package main

import (
	"bytes"
	"github.com/klauspost/compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"mime"
	neturl "net/url"
	"os"
	"path"
	fp "path/filepath"
	"runtime"
	"strings"
	"time"

	http "github.com/siadat/gofile/http"
)

var (
	rootDir   = ""
	startTime = time.Now()
	history   []string
)

func htmlLayoutTop(title string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
    <html>
    <head>
      <title>%s | Gofile</title>
      <meta charset="utf-8" />
	  <style>
	    body { font-family:monospace; padding:0; margin:0; }
	    h1,h2,h3,h4,h5,h6 { margin:10px; padding:10px; }
	    ul { padding:0; margin:0; }
	    li { padding:0; margin:1px 0; background-color:#eee; list-style:none; }
	    li.dir { background-color:#B8D2E8; font-weight:bold; /*eea;*/ }
	    li a { color:#29414E; text-decoration:none; display:block; padding:5px 20px; }
	  </style>
    </head>
    <body>`, title)
}
func htmlLayoutBottom() string {
	return `</body></html>`
}

func htmlLink(href, text string) string {
	return fmt.Sprintf("<a href='%s'>%s</a>", href, text)
}

func filesLis(fileInfos []os.FileInfo, url string, urlUnescaped string, bodyChan chan []byte) {
	bodyChan <- []byte("<ul>")
	for _, fi := range fileInfos {
		filename := fi.Name()
		class := ""
		if fi.IsDir() {
			class = " class='dir' "
			filename = filename + "/"
		}

		fullPath := strings.Join([]string{url, neturl.QueryEscape(fi.Name())}, "/")
		// url could end with a "/" or with no "/", so when joined with
		// something else using "/" there could be a double slash ie "//"
		fullPath = strings.Replace(fullPath, "//", "/", 1)

		bodyChan <- []byte(fmt.Sprintf("<li%s>%s</li>\n", class, htmlLink(fullPath, filename)))
	}
	bodyChan <- []byte("</ul>")
	return
}

func listDirChan(url string, urlUnescaped string, filepath string, res *http.Response) (err error) {
	res.Body <- []byte(htmlLayoutTop(url))
	res.Body <- []byte(fmt.Sprintf("<h1>Directory Listing for %s</h1>", urlUnescaped))
	if false {
		res.Body <- []byte(fmt.Sprintf("<h3>Uptime:%s Goroutines:%d Requests:%d</h3>",
			time.Since(startTime),
			runtime.NumGoroutine(),
			len(history),
		))
	}

	fileInfos, status, errMsg := getFilesInDir(filepath)
	filesLis(fileInfos, url, urlUnescaped, res.Body)
	res.Body <- []byte(htmlLayoutBottom())

	if status != 200 {
		err = errors.New(errMsg)
		// content = ""
	}

	return
}

func getRootDir(optRoot string) (root string) {
	var wd string
	if len(optRoot) == 0 {
		wd, _ = os.Getwd()
	} else {
		wd = optRoot
	}

	root, err := fp.Abs(wd)

	if err != nil {
		panic(err)
	}
	return
}

func tryFilepaths(filepath string) (filepathRet string, file os.FileInfo, err error) {
	filepaths := []string{
		filepath,
		fmt.Sprintf("%s.html", filepath),
		fmt.Sprintf("%s.htm", filepath),
		fmt.Sprintf("%s/index.html", filepath),
		fmt.Sprintf("%s/index.htm", filepath),
	}

	for _, filepathRet = range filepaths {
		file, err = os.Stat(filepathRet)
		if err == nil {
			return filepathRet, file, nil
		}
	}

	return filepathRet, file, err
}

func urlToFilepath(requestURI string) (requestedFilepath string, retErr error) {
	// Note: path.Join removes '..', so the HasPrefix check is safe for paths
	// that try to traverse parent directory using '..'.
	if len(rootDir) == 0 {
		rootDir = getRootDir("")
	}
	requestedFilepath = path.Join(rootDir, requestURI)

	if !strings.HasPrefix(requestedFilepath, rootDir) {
		retErr = errors.New("Requested URI is not allowed")
		requestedFilepath = ""
		return
	}
	return
}

func getFilesInDir(requestedFilepath string) (fileInfos []os.FileInfo, status int, errMsg string) {
	status = 200
	fileInfos, err := ioutil.ReadDir(requestedFilepath)

	if err != nil {
		errMsg = "Requested URI was not found."
		status = 404
	}

	return
}

func getFilesize(filepath string) (fileSize int64) {
	f, err := os.Open(filepath)
	if err != nil {
		return
	}
	defer f.Close()

	if fi, err := f.Stat(); err == nil {
		fileSize = fi.Size()
	}
	return
}

func downloadFileChan(filepath string, ranges []http.ByteRange, res *http.Response) (err error) {
	// NOTE at the moment we are respecting the first range only
	rangeFrom := ranges[0].Start
	rangeTo := ranges[0].End

	f, err := os.Open(filepath)
	if err != nil {
		return
	}
	defer f.Close()

	var fileSize int64
	if fi, err := f.Stat(); err == nil {
		fileSize = fi.Size()
	}

	if rangeTo < 0 {
		rangeTo = fileSize + rangeTo
	}

	if rangeFrom < 0 {
		rangeFrom = fileSize + rangeFrom
	}

	rangeFromNew, err := f.Seek(rangeFrom, 0)
	if err != nil {
		f.Close()
		return
	}

	if rangeTo < rangeFromNew {
		rangeTo = rangeFromNew
	}

	var maxFileReadLen int64 = 1024 * 1024
	for cursorFrom := rangeFromNew; cursorFrom <= rangeTo; cursorFrom += maxFileReadLen {
		cursorTo := cursorFrom + maxFileReadLen - 1
		if cursorTo > rangeTo {
			cursorTo = rangeTo
		}
		buff := make([]byte, cursorTo-cursorFrom+1)

		_, err := f.Seek(cursorFrom, 0)
		if err != nil {
			break
		}

		readN, readErr := f.Read(buff)
		if readErr != nil {
			break
		}
		if readN == 0 {
			break
		}

		res.Body <- buff[:readN]
		// time.Sleep(time.Millisecond * 10)
	}

	return
}

func downloadGZipChan(filepath string, res *http.Response) (err error) {
	f, err := os.Open(filepath)
	if err != nil {
		return
	}
	defer f.Close()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	n, err := io.Copy(gw, f)
	if err != nil {
		return
	}
	err = gw.Close()
	if err != nil {
		return
	}
	log.Printf("gzip orginal size is %v, compressed size is %v", n, buf.Len())
	res.ContentLength = int64(buf.Len())
	res.Body <- buf.Bytes()

	return
}


func fileServerHandleRequestGen(optRoot string) func(http.Request, *http.Response) {
	rootDir = getRootDir(optRoot)
	return fileServerHandleRequest
}

func fileServerHandleRequest(req http.Request, res *http.Response) {
	history = append(history, req.URL.Path)

	unescapedURL, _ := neturl.QueryUnescape(req.URL.Path)
	filepath, err := urlToFilepath(unescapedURL)
	if err != nil {
		res.Status = 401
		defer close(res.Body)
		res.Body <- []byte(err.Error() + "\n")
		return
	}

	filepath, file, err := tryFilepaths(filepath)
	if err != nil {
		res.Status = 404
		defer close(res.Body)
		res.Body <- []byte("")
		return
	}

	if file.IsDir() {
		res.Status = 200
		res.ContentType = "text/html"
		defer close(res.Body)
		err = listDirChan(req.URL.Path, unescapedURL, filepath, res)
		// if err != nil { res.Status = 400 }
		return
	}

	res.Status = 200
	fileIsModified := true
	if len(req.Headers["If-Modified-Since"]) > 0 {
		ifModifiedSince := http.ParseHTTPDate(req.Headers["If-Modified-Since"])
		if !file.ModTime().After(ifModifiedSince) {
			fileIsModified = false
		}
	}

	if fileIsModified {
		res.ContentType = mime.TypeByExtension(fp.Ext(filepath))
		if req.Headers["Accept-Encoding"] == "gzip" {
			res.ContentEncoding = "gzip"
			err = downloadGZipChan(filepath, res)
		} else {
			res.ContentLength = getFilesize(filepath)
			err = downloadFileChan(filepath, req.Ranges, res)
		}
	} else {
		res.Status = 304
	}

	defer close(res.Body)

	if err != nil {
		res.Status = 400
	}
}
