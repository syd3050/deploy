package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"sync"
	"time"
	"unicode/utf8"

	"github.com/Unknwon/goconfig"
	"github.com/fsnotify/fsnotify"

	_ "net/http/pprof"
)

/**
 *   NotifyFile ..
 */
type NotifyFile struct {
	watch    *fsnotify.Watcher
	modifyCh chan changeInfo
	ignore   []string
}

var hmlock *sync.RWMutex

//变更类型:1 新增; 2删除; 3修改; 4重命名
type eventType int8

const (
	EV_ADD    eventType = 1
	EV_DEL    eventType = 2
	EV_UPDATE eventType = 3
	EV_RENAME eventType = 4
	EV_CHMOD  eventType = 5
)

type changeInfo struct {
	//目录或文件路径
	cPath string
	//是否是文件
	isFile bool
	//变更类型:1 新增; 2删除; 3修改; 4重命名
	cType eventType
}

/**
*
 */
func NewNotifyFile() *NotifyFile {
	w := new(NotifyFile)
	w.watch, _ = fsnotify.NewWatcher()
	w.modifyCh = make(chan changeInfo, 100)
	return w
}

func IsContain(items []string, item string) bool {
	for _, eachItem := range items {
		if eachItem == item {
			return true
		}
	}
	return false
}

// 判断所给路径文件/文件夹是否存在
func Exists(path string) bool {
	_, err := os.Stat(path) //os.Stat获取文件信息
	if err != nil {
		if os.IsExist(err) {
			return true
		}
		return false
	}
	return true
}

/**
*
 */
func (notify *NotifyFile) WatchDir(dir string) {
	//通过Walk来遍历目录下的所有子目录
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if exist := Exists(path); !exist {
			return err
		}
		if path == "." || path == ".." {
			return err
		}
		//判断是否为目录，监控目录,目录下文件也在监控范围内，不需要加
		if info.IsDir() && strings.Index(path, ".git") == -1 && strings.Index(path, ".idea/") == -1 && strings.Index(path, ".DS") == -1 {
			path, err := filepath.Abs(path)
			if err != nil {
				return err
			}
			if isIgnore := IsContain(notify.ignore, path); !isIgnore {
				err = notify.watch.Add(path)
				if err != nil {
					return err
				}
			}
		} else if strings.Index(path, ".git") != -1 || strings.Index(path, "~") != -1 || strings.Index(path, "___") != -1 || strings.Index(path, ".idea/") != -1 {
			notify.watch.Remove(path)
		}

		return nil
	})

	go notify.WatchEvent()
}

//var finishedMap map[string]int64
var eventCache map[string]changeInfo
var evCacheLock *sync.RWMutex

// func clearOld() {
// 	for {
// 		for len(finishedMap) == 0 {
// 			//5秒查看一次
// 			time.Sleep(time.Duration(5) * time.Second)
// 		}
// 		for key, thatTime := range finishedMap {
// 			if time.Now().Unix()-thatTime > 10 {
// 				hmlock.Lock()
// 				delete(finishedMap, key)
// 				hmlock.Unlock()
// 			}
// 		}
// 	}
// }

func addEventBack() {
	for key, eventInfo := range eventCache {
		select {
		case watch.modifyCh <- eventInfo:
			evCacheLock.Lock()
			delete(eventCache, key)
			evCacheLock.Unlock()
			fmt.Printf("Readd event to channel:%v \n", eventInfo)
			time.Sleep(300 * time.Millisecond)
		case <-time.After(500 * time.Millisecond):
		}
	}
}

/**
*
 */
func (notify *NotifyFile) WatchEvent() {
	var ignoreTag = []string{".swp", ".git", "___", "~", ".idea", ".D"}
	for {
		select {
		case ev := <-notify.watch.Events:
			{
				//fmt.Printf("Event:%v ,path:%v \n", ev.Op, ev.Name)
				var cInfo changeInfo
				cInfo.cPath = ev.Name
				cInfo.isFile = true
				if ev.Op&fsnotify.Create == fsnotify.Create {
					fmt.Println("创建文件 : ", ev.Name)
					//获取新创建文件的信息，如果是目录，则加入监控中
					file, err := os.Stat(ev.Name)
					//新增
					cInfo.cType = EV_ADD
					if err == nil && file.IsDir() {
						cInfo.isFile = false
						//在重命名目录时，新名称的目录就是创建操作，这时需要把其目录下所有文件都视为新增文件
						renameDir(ev.Name, notify.modifyCh)
						notify.watch.Add(ev.Name)
						//fmt.Printf("对目录{%s}添加监控 \n ", ev.Name)
					}
					/*记录新创建文件时间，下面删除文件的时候需要判断，因为不同的IDE对文件的修改行为表现不一致，有些IDE是先创建新文件，然后删除旧文件， */
					//所以下面判断删除通知时，要屏蔽这种删除操作，因为其本质上是创建行为，如果不屏蔽，会把删除消息当做正常消息处理，就会导致删除了一个本来只需要修改的文件
					// hmlock.RLock()
					// finishedMap[ev.Name] = time.Now().Unix()
					// hmlock.RUnlock()
				}

				if ev.Op&fsnotify.Write == fsnotify.Write {
					//修改
					cInfo.cType = EV_UPDATE
					fmt.Println("文件更新 : !!!!!!!", ev.Name)
				}

				// if ev.Op&fsnotify.Rename == fsnotify.Rename {
				// 	//如果重命名文件是目录，则移除监控 ,注意这里无法使用os.Stat来判断是否是目录了
				// 	//因为重命名后，go已经无法找到原文件来获取信息了,所以简单粗爆直接remove
				// 	fmt.Println("重命名文件 : ", ev.Name)
				// 	//注意，这里的ev.Name是原来的文件或者目录名
				// 	cInfo.cType = EV_RENAME
				// 	// fi, err := os.Stat(ev.Name)
				// 	// if err == nil && fi.IsDir() {
				// 	// 	renameDir(ev.Name, notify.modifyCh)
				// 	// }

				// 	notify.watch.Remove(ev.Name)
				// }
				if ev.Op&fsnotify.Remove == fsnotify.Remove {
					// hmlock.RLock()
					// thatTime, isExist := finishedMap[ev.Name]
					// hmlock.RUnlock()
					//10秒前的操作不记录
					// if !isExist || (isExist && time.Now().Unix()-thatTime > 10) {
					// 	cInfo.cType = EV_DEL
					// 	//如果删除文件是目录，则移除监控
					// 	fi, err := os.Stat(ev.Name)
					// 	if err == nil && fi.IsDir() {
					// 		cInfo.isFile = false
					// 		notify.watch.Remove(ev.Name)
					// 		//fmt.Println("删除监控 : ", ev.Name)
					// 	}
					// 	fmt.Printf("删除文件 %s,type:%v,op=%v,remove=%v,rename=%v \n", ev.Name,
					// 		cInfo.cType, ev.Op, fsnotify.Remove, fsnotify.Rename)
					// }
				}

				if ev.Op&fsnotify.Chmod == fsnotify.Chmod {
					//cInfo.cType = EV_CHMOD
					//fmt.Println("修改权限 : ", ev.Name)

				}
				//fmt.Printf("insert into chan %v \n", cInfo)
				notContain := notContains(ev.Name, ignoreTag)
				if notContain && (ev.Op&fsnotify.Chmod != fsnotify.Chmod) {
					select {
					case notify.modifyCh <- cInfo:
						fmt.Printf("Add event to channel:%v \n", cInfo)
					case <-time.After(100 * time.Millisecond):
						evCacheLock.Lock()
						randStr := "_" + RandomString(5)
						eventCache[ev.Name+randStr] = cInfo
						evCacheLock.Unlock()
					}

				}
			}
		case err := <-notify.watch.Errors:
			{
				fmt.Println("error : ", err)
				return
			}
		}
	}
}

var defaultLetters = []rune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789")

func RandomString(n int, allowedChars ...[]rune) string {
	var letters []rune

	if len(allowedChars) == 0 {
		letters = defaultLetters
	} else {
		letters = allowedChars[0]
	}

	b := make([]rune, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}

	return string(b)
}

func notContains(hack string, needleArr []string) bool {
	for _, tag := range needleArr {
		if strings.Index(hack, tag) != -1 {
			return false
		}
	}
	return true
}

func renameDir(oldPath string, channel chan changeInfo) {
	fmt.Printf("in rename,path=%v \n", oldPath)
	filepath.Walk(oldPath, func(path string, info os.FileInfo, err error) error {
		fmt.Printf("in dir,file=%v,isDir?%v \n", path, info.IsDir())
		if oldPath == path {
			return nil
		}
		//判断是否为目录，如果是目录，目录下文件也在传输范围内
		if info.IsDir() && strings.Index(path, ".") == -1 {
			renameDir(path, channel)
		} else {
			var cInfo changeInfo
			cInfo.cPath = path
			cInfo.isFile = true
			cInfo.cType = EV_ADD
			channel <- cInfo
		}

		return nil
	})
}

// Creates a new file upload http request with optional extra params
func newfileUploadRequest(uri string, params map[string]string, path string) (*http.Request, error) {
	file, err := os.Open(path)
	defer file.Close()
	if err != nil {
		return nil, err
	}

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("userfile", filepath.Base(path))
	if err != nil {
		writer.Close()
		return nil, err
	}
	_, err = io.Copy(part, file)

	for key, val := range params {
		_ = writer.WriteField(key, val)
	}
	err = writer.Close()
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequest("POST", uri, body)
	request.Header.Add("Content-Type", writer.FormDataContentType())
	return request, err
}

func buildPostReq(uri string, reqBody map[string]string) (*http.Request, error) {
	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	for key, val := range reqBody {
		_ = writer.WriteField(key, val)
	}
	err := writer.Close()
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequest("POST", uri, body)
	request.Header.Add("Content-Type", writer.FormDataContentType())
	return request, err
}

type Account struct {
	username string
	passwrod string
}

func md5V1(str string) string {
	h := md5.New()
	h.Write([]byte(str))
	cipherStr := h.Sum(nil)
	return hex.EncodeToString(cipherStr)
}

type command struct {
	token    string
	time     string
	cmdType  string
	cmdStr   string
	basePath string
	isFile   bool
}

func getCmdType(eType eventType) string {
	//fmt.Printf("type is %v \n", eType)
	if eType == EV_ADD {
		return "add"
	}
	if eType == EV_UPDATE {
		return "add"
	}
	if eType == EV_DEL {
		return "del"
	}
	if eType == EV_RENAME {
		return "rename"
	}
	return ""
}

func buildCmd(chInfo *changeInfo, localPath, targetPath string) map[string]string {
	cmd := make(map[string]string)
	//timeUnix := fmt.Sprintf("%v", time.Now().Unix())
	// cmd["token"] = md5V1(infoCache.account.username + timeUnix + infoCache.account.passwrod)
	// cmd["time"] = timeUnix
	cmd["cmdType"] = getCmdType(chInfo.cType)
	cmd["target"] = strings.Replace(chInfo.cPath, localPath, targetPath, 1)
	return cmd
}

func parseLocal2Remote(cPath string) (string, string) {
	local := ""
	len := 0
	//寻找相对于事件文件来讲，所有需要处理的远程路径
	for watchDir, _ := range watchMap {
		nLen := utf8.RuneCountInString(watchDir)
		//找最长路径
		if strings.Index(cPath, watchDir) != -1 && nLen > len {
			local = watchDir
			len = nLen
		}
	}
	if len == 0 {
		return "", ""
	}
	len = 0
	remoteLc := ""
	lcPath := ""
	for lc, rm := range watchMap[local] {
		nLen := utf8.RuneCountInString(lc)
		//fmt.Printf("-------cPath=%s,local+lc=%s----------,\n",chInfo.cPath, local+lc)
		//找最长路径
		if strings.Index(cPath, local+lc) != -1 && nLen > len {
			lcPath = local + lc
			remoteLc = rm
			len = nLen
			//fmt.Println("xxxxxxx", local+lc,nLen, watchMap[local], "xxxxxxxx")
			//fmt.Println(nLen,local,lcPath,rm)
		}
	}
	return lcPath, remoteLc + "/"
}

func uploadFile(chInfo *changeInfo) {

	lcPath, targetPath := parseLocal2Remote(chInfo.cPath)
	cmd := buildCmd(chInfo, lcPath, targetPath)
	//fmt.Printf(".......执行uploadFile命令，命令内容:%v ........ \n", cmd)

	uploadAll(cmd, chInfo.cPath)
}

func uploadAll(cmd map[string]string, lcPath string) bool {
	file, err := os.Stat(lcPath)
	if lcPath == "." || lcPath == ".." {
		return false
	}
	if err == nil && file.IsDir() && strings.Index(lcPath, ".git") == -1 {
		request, err := newfileUploadRequest(
			deployURL,
			cmd,
			lcPath)
		if err != nil {
			log.Fatal(err)
		}
		client := &http.Client{}
		resp, err := client.Do(request)
		if err != nil {
			log.Fatal(err)
		} else {
			body := &bytes.Buffer{}
			_, err := body.ReadFrom(resp.Body)
			if err != nil {
				log.Fatal(err)
			}
			resp.Body.Close()
			fmt.Printf("StatusCode:%d \n Header:%v \n Body:%v \n", resp.StatusCode, resp.Header, body)
		}

		filepath.Walk(lcPath, func(path string, info os.FileInfo, err error) error {
			if exist := Exists(path); !exist {
				return err
			}
			if path == "." || path == ".." || lcPath == path || strings.Index(path, ".git") != -1 {
				return err
			}
			lcPath, targetPath := parseLocal2Remote(path)
			cmd["target"] = strings.Replace(path, lcPath, targetPath, 1)
			uploadAll(cmd, path)
			return nil
		})
	} else if strings.Index(lcPath, ".DS") == -1 && strings.Index(lcPath, ".git") == -1 {
		request, err := newfileUploadRequest(
			deployURL,
			cmd,
			lcPath)
		if err != nil {
			log.Fatal(err)
		}
		client := &http.Client{}
		resp, err := client.Do(request)
		if err != nil {
			log.Fatal(err)
		} else {
			body := &bytes.Buffer{}
			_, err := body.ReadFrom(resp.Body)
			if err != nil {
				log.Fatal(err)
			}
			resp.Body.Close()
			fmt.Printf("StatusCode:%d \n Header:%v \n Body:%v \n", resp.StatusCode, resp.Header, body)
		}
	}
	return true
}

type InfoCache struct {
	targetPath string
	targetURL  string
	account    Account
	localPath  string
}

func handleEvent(chInfo *changeInfo) error {
	var err = errors.New("类型不合法")
	if strings.Index(chInfo.cPath, "___") != -1 {
		return nil
	}
	fmt.Printf("handleEvent chInfo %v .......\n", chInfo)
	if chInfo.cType == EV_UPDATE || chInfo.cType == EV_ADD {
		uploadFile(chInfo)
	} else if chInfo.cType == EV_DEL {
		postReq(chInfo)
	}
	//防止消息过多，PHP处理不过来，这个PHP最多每秒50个处理
	time.Sleep(time.Duration(20) * time.Millisecond)
	return err
}

func postReq(chInfo *changeInfo) {
	lcPath, targetPath := parseLocal2Remote(chInfo.cPath)
	cmd := buildCmd(chInfo, lcPath, targetPath)
	request, err := buildPostReq(deployURL, cmd)
	if err != nil {
		log.Fatal(err)
	}
	client := &http.Client{}
	resp, err := client.Do(request)
	if err != nil {
		log.Fatal(err)
	} else {
		body := &bytes.Buffer{}
		_, err := body.ReadFrom(resp.Body)
		if err != nil {
			log.Fatal(err)
		}
		resp.Body.Close()
		fmt.Printf("StatusCode:%d \n Header:%v \n Body:%v \n", resp.StatusCode, resp.Header, body)
	}
}

func updateConfig(w http.ResponseWriter, r *http.Request) {
	getParamsMap := r.URL.Query()
	finish := 0
	if localPath, ok := getParamsMap["lp"]; ok {
		infoCache.localPath = localPath[0]
		finish++
		io.WriteString(w, fmt.Sprintf("localDir:%s \n", infoCache.localPath))
		fmt.Printf("localDir:%s \n", infoCache.localPath)
	}
	if targetPath, ok := getParamsMap["tp"]; ok {
		infoCache.targetPath = targetPath[0]
		finish++
		io.WriteString(w, fmt.Sprintf("targetPath:%s \n", infoCache.targetPath))
		fmt.Printf("targetPath:%s \n", infoCache.targetPath)
	}
	if targetURL, ok := getParamsMap["tu"]; ok {
		infoCache.targetURL = targetURL[0]
		finish++
		io.WriteString(w, fmt.Sprintf("targetURL:%s \n", infoCache.targetURL))
		fmt.Printf("targetURL:%s \n", infoCache.targetURL)
	}
	if finish == 3 {
		hasInit <- true
	}

	io.WriteString(w, fmt.Sprintf("Done! We get request params:%v \n", getParamsMap))
	fmt.Printf("%v \n", getParamsMap)
}

func runServer() {
	http.HandleFunc("/update", updateConfig)
	http.ListenAndServe(":9999", nil)
}

var infoCache InfoCache
var localDir string

var hasInit chan bool
var deployURL string

func main() {
	defer watch.watch.Close()
	loadConfig()

	startWatch()

	exit := make(chan bool)

	go evLoop()
	//go clearOld()
	go addEventBack()

	// runtime.GOMAXPROCS(1)              // 限制 CPU 使用数，避免过载
	// runtime.SetMutexProfileFraction(1) // 开启对锁调用的跟踪
	// runtime.SetBlockProfileRate(1)     // 开启对阻塞操作的跟踪

	// http.ListenAndServe("0.0.0.0:6060", nil)
	<-exit
	runtime.Goexit()
}

func evLoop() {
	for {
		select {
		case chInfo := <-watch.modifyCh:
			//fmt.Println(chInfo)
			handleEvent(&chInfo)
		case <-time.After(60 * time.Second):
		}

	}
}

var watchMap map[string]map[string]string
var watch *NotifyFile

func init() {
	watch = NewNotifyFile()
	watch.ignore = make([]string, 4)
	//	finishedMap = make(map[string]int64, 250)
	hmlock = &sync.RWMutex{}
	evCacheLock = &sync.RWMutex{}
	eventCache = make(map[string]changeInfo, 100)
}

func startWatch() {
	for localPath := range watchMap {
		watch.WatchDir(localPath)
	}
}

func loadConfig() (map[string]string, map[string]map[string]string, map[string]map[string]string) {
	cfg, err := goconfig.LoadConfigFile("./conf.ini")
	if err != nil {
		log.Fatalf("无法加载配置文件：%s", err)
	}
	sections := cfg.GetSectionList()
	commonMap := make(map[string]string)
	cfgMap := make(map[string]map[string]string)
	//子节点，最外层key为父节点字段名
	childrenMap := make(map[string]map[string]string)
	watchMap = make(map[string]map[string]string)
	//每个父节点中配置的local_path映射
	localPathMap := make(map[string]string)
	for _, v := range sections {
		keys := cfg.GetKeyList(v)
		//处理common配置
		if v == "common" {
			for _, ck := range keys {
				commonMap[ck], _ = cfg.GetValue(v, ck)
			}
			continue
		}
		//是子节点配置项
		if strings.Index(v, ".") != -1 {
			parent2child := strings.Split(v, ".")
			//parent2child第一项为父节点名称
			childrenMap[parent2child[0]] = make(map[string]string, 0)
			//将配置项全部记录到父节点下面
			for _, secKey := range keys {
				secVal, _ := cfg.GetValue(v, secKey)
				childrenMap[parent2child[0]][secKey] = secVal
				//寻找父节点对应的local_path
				lp := ""
				ok := false
				if lp, ok = localPathMap[parent2child[0]]; !ok {
					log.Fatalf("父节点 %s 必须在子节点 %s之上 \n", parent2child[0], v)
				}
				watchMap[lp]["/"+secKey] = secVal
			}
			continue
		}
		localBase := ""
		cfgMap[v] = make(map[string]string, 0)
		for _, secKey := range keys {
			secVal, _ := cfg.GetValue(v, secKey)
			//记录本地需要监控的目录
			if secKey == "local_base" {
				localBase = secVal
				watchMap[secVal] = make(map[string]string, 0)
				//记录该父节点上配置的local_path
				localPathMap[v] = secVal
			} else if secKey == "remote_base" {
				watchMap[localBase]["/"] = secVal
			} else if secKey == "ignore" {
				ignorePath := localBase + "/" + secVal
				watch.ignore = append(watch.ignore, ignorePath)
			}
			cfgMap[v][secKey] = secVal
		}
	}
	checkConfig(commonMap)
	//fmt.Printf("map is %v \n child map is %v \n", cfgMap, childrenMap)
	return commonMap, cfgMap, childrenMap
}

func checkConfig(cfg map[string]string) {
	var ok bool
	if deployURL, ok = cfg["deploy_url"]; !ok {
		log.Fatal("common段必须包含deploy_url字段")
	}
	if deployURL == "" {
		log.Fatal("common段deploy_url字段不能为空")
	}

}
