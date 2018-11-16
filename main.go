package main

import (
	"crypto/md5"
	"flag"
	"fmt"
	"github.com/hanwen/go-fuse/fuse"
	"github.com/hanwen/go-fuse/fuse/nodefs"
	"github.com/hanwen/go-fuse/fuse/pathfs"
	"gopkg.in/gographics/imagick.v3/imagick"
	"log"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var thumbnailRegex = regexp.MustCompile(`\w{2}\/\w{2}\/\w{2}\/([\w-]+)_(\d+)x(\d+)(@2x)?\.(\w+)`)

type FuseMount struct {
	pathfs.FileSystem
	srcPoint pathfs.FileSystem
}

func ReplaceBadWords(input string) (output string) {
	return strings.Replace(input, "/ad/", "/g0/", -1)
}

func Encode(name string) (encodedName string) {
	var md5String = fmt.Sprintf("%x", md5.Sum([]byte("media/image/"+name)))

	return ReplaceBadWords(string(md5String[0:2]) + "/" + string(md5String[2:4]) + "/" + string(md5String[4:6]) + "/" + name)
}

func DecodeThumbnailName(encodedPath string) (fileName string, height int, width int, retina bool) {
	var regexResult = thumbnailRegex.FindAllStringSubmatch(encodedPath, -1)

	if regexResult[0][4] == "@2x" {
		retina = true
	}

	fileName = regexResult[0][1] + "." + regexResult[0][5]
	height, _ = strconv.Atoi(regexResult[0][2])
	width, _ = strconv.Atoi(regexResult[0][3])

	return
}

func isThumbnail(path string) (encoded bool) {
	return thumbnailRegex.MatchString(path)
}

func GetGeneratedSubfolderNames() (subFolderNames []string) {
	for i := 0; i < 16; i++ {
		var directories [16] string

		for a := 0; a < 16; a++ {
			directories[a] = fmt.Sprintf("%x%x", byte(i), byte(a))
		}

		subFolderNames = append(subFolderNames, directories[0:16]...)
	}

	return
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func (fuseMount *FuseMount) GetAttr(name string, context *fuse.Context) (*fuse.Attr, fuse.Status) {
	fmt.Println("GetAttr: " + name)

	if name == "" {
		return &fuse.Attr{
			Mode: fuse.S_IFDIR | 0755,
		}, fuse.OK
	}

	srcAttr, srcStatus := fuseMount.srcPoint.GetAttr(name, context)

	if srcStatus.Ok() {
		return srcAttr, srcStatus
	} else if isThumbnail(name) {
		fileName, _, _, _ := DecodeThumbnailName(name)
		originalFileName := Encode(fileName)

		originalAttr, originalStatus := fuseMount.srcPoint.GetAttr(originalFileName, context)
		if originalStatus.Ok() {
			return originalAttr, originalStatus
		}
	}

	var dirParts = strings.Split(name, "/")
	if len(dirParts) < 4 && contains(GetGeneratedSubfolderNames(), dirParts[len(dirParts)-1]) {
		return &fuse.Attr{
			Mode: fuse.S_IFDIR | 0755,
		}, fuse.OK
	}

	return nil, fuse.ENOENT
}

func (fuseMount *FuseMount) OpenDir(name string, context *fuse.Context) (dirEntries []fuse.DirEntry, code fuse.Status) {
	fmt.Println("OpenDir: " + name)

	srcDirEntries, srcStatus := fuseMount.srcPoint.OpenDir(name, context)

	var dirParts = strings.Split(name, "/")
	if len(dirParts) < 3 {
		for _, generatedSubFolder := range GetGeneratedSubfolderNames() {
			dirEntries = append(dirEntries, fuse.DirEntry{Name: generatedSubFolder, Mode: fuse.S_IFDIR})
		}
	}

	if srcStatus.Ok() {
		dirEntries = append(dirEntries, srcDirEntries...)
	}

	return dirEntries, fuse.OK
}

func (fuseMount *FuseMount) Open(name string, flags uint32, context *fuse.Context) (file nodefs.File, code fuse.Status) {
	fmt.Println("Open: " + name)

	if flags&fuse.O_ANYWRITE != 0 {
		return nil, fuse.EPERM
	}

	srcFile, srcStatus := fuseMount.srcPoint.Open(name, flags, context)

	fmt.Println("direct access: " + srcStatus.String())

	if srcStatus.Ok() {
		return srcFile, srcStatus
	} else if isThumbnail(name) {
		fileName, height, width, _ := DecodeThumbnailName(name)
		originalFileName := Encode(fileName)

		fmt.Println("Generating Thumbnail for: " + originalFileName)

		originalFile, originalStatus := fuseMount.srcPoint.Open(originalFileName, flags, context)
		if originalStatus.Ok() {
			imagick.Initialize()
			defer imagick.Terminate()

			mw := imagick.NewMagickWand()

			originalFileAttr, _ := fuseMount.srcPoint.GetAttr(originalFileName, context)
			rawImage := make([]byte, originalFileAttr.Size)
			rr, _ := originalFile.Read(rawImage, 0)
			rr.Bytes(rawImage)

			mw.ReadImageBlob(rawImage)
			mw.SetImageFormat("JPG")
			mw.ScaleImage(uint(width), uint(height))
			mw.SetCompressionQuality(uint(5))
			mw.ResetIterator()

			var folderLevels = strings.Split(name, "/")
			for key := range folderLevels {
				if key == len(folderLevels) -1 {
					continue
				}

				var folderString = strings.Join(folderLevels[0:key + 1], "/")

				_, status := fuseMount.srcPoint.GetAttr(folderString, context)

				if !status.Ok() {
					fuseMount.srcPoint.Mkdir(folderString, 0755, context)
				}
			}

			file, code = fuseMount.srcPoint.Create(name, uint32(os.O_CREATE|os.O_RDWR), 0644, context)
			fmt.Println("File creation: " + name + " | " + code.String())

			var imageData = mw.GetImageBlob()
			writtenBytes, status := file.Write(imageData, 0)
			file.Flush()

			fmt.Print(writtenBytes)
			fmt.Print(status.String())

			defer mw.Destroy()

			return file, originalStatus
		}
	}

	return nil, fuse.ENOENT
}


func main() {
	flag.Parse()
	if len(flag.Args()) < 2 {
		log.Fatal("Usage:\n MOUNTPOINT SRCPOINT")
	}

	loopBackFS := pathfs.NewLoopbackFileSystem(flag.Arg(1))

	nfs := pathfs.NewPathNodeFs(&FuseMount{FileSystem: pathfs.NewDefaultFileSystem(), srcPoint: loopBackFS}, nil)
	server, _, err := nodefs.MountRoot(flag.Arg(0), nfs.Root(), nil)
	if err != nil {
		log.Fatalf("Mount fail: %v\n", err)
	}

	server.Serve()
}
