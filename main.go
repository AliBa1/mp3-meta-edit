package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bogem/id3v2"
	"github.com/disintegration/imaging"
)

var stdin = bufio.NewReader(os.Stdin)

// to run: go run . [drag file or folder into terminal]
// to build: go build -o mp3metaedit .
// to run executable (Mac/Linux): ./mp3metaedit [file or folder path]
// to run executable (Windows): mp3metaedit.exe [file or folder path]

// ---- Lists (manually edited at mp3metaedit_lists.json next to the executable) ----

type Lists struct {
	Artists []string `json:"artists"`
	Albums  []string `json:"albums"`
}

var lists Lists

func listsPath() string {
	exe, err := os.Executable()
	if err == nil {
		return filepath.Join(filepath.Dir(exe), "mp3metaedit_lists.json")
	}
	return "mp3metaedit_lists.json"
}

func loadLists() {
	data, err := os.ReadFile(listsPath())
	if err != nil {
		return // file doesn't exist yet
	}
	json.Unmarshal(data, &lists)
}

// coversDir returns the covers/ folder next to the executable.
func coversDir() string {
	exe, err := os.Executable()
	if err == nil {
		return filepath.Join(filepath.Dir(exe), "covers")
	}
	return "covers"
}

// listCovers returns image filenames found in the covers/ folder.
func listCovers() []string {
	entries, err := os.ReadDir(coversDir())
	if err != nil {
		return nil
	}
	var covers []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := strings.ToLower(e.Name())
		if strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") || strings.HasSuffix(name, ".png") {
			covers = append(covers, e.Name())
		}
	}
	return covers
}

// promptFromList shows a numbered list and lets the user pick or type a new value.
func promptFromList(label string, items []string, validate func(string) bool) string {
	if len(items) > 0 {
		fmt.Println(label + ":")
		for i, v := range items {
			fmt.Printf("  %d: %s\n", i+1, v)
		}
	}
	fmt.Print("Enter number to select, or type a new " + label + ": ")

	input, err := stdin.ReadString('\n')
	input = strings.TrimSpace(input)
	input = strings.Trim(input, "'")

	if err != nil || input == "" {
		fmt.Println(label + " cannot be empty!")
		return promptFromList(label, items, validate)
	}

	if n, err := strconv.Atoi(input); err == nil {
		if n >= 1 && n <= len(items) {
			return items[n-1]
		}
		fmt.Printf("Invalid selection: choose 1-%d or type a new value\n", len(items))
		return promptFromList(label, items, validate)
	}

	if validate != nil && !validate(input) {
		return promptFromList(label, items, validate)
	}

	return input
}

// ---- Main ----

func printHelp() {
	fmt.Println("Usage: mp3metaedit <file|folder>")
	fmt.Println()
	fmt.Println("Single file mode:")
	fmt.Println("  Edit title, artist, album, and cover art for one MP3 file.")
	fmt.Println()
	fmt.Println("Folder mode:")
	fmt.Println("  Edit artist, album, and cover art for all MP3 files in a folder.")
	fmt.Println()
	fmt.Println("Cover art:")
	fmt.Println("  Place images in a covers/ folder next to the executable for quick selection.")
	fmt.Println("  Images are resized to 300x300 automatically.")
	fmt.Println()
	fmt.Println("Lists:")
	fmt.Println("  Add preset artists and albums to mp3metaedit_lists.json next to the executable:")
	fmt.Println(`  { "artists": ["Artist A", "Artist B"], "albums": ["Album A"] }`)
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("A file or folder needs to be provided as an argument")
	}
	path := os.Args[1]
	if path == "--help" || path == "-h" || path == "help" {
		printHelp()
		return
	}
	path = strings.Trim(path, "'")

	info, err := os.Stat(path)
	if err != nil {
		log.Fatalf("Failed to access path: %v", err)
	}

	loadLists()

	if info.IsDir() {
		folderMode(path)
	} else {
		fileMode(path)
	}
}

// sanitizeFilename replaces characters not allowed in filenames.
func sanitizeFilename(name string) string {
	replacer := strings.NewReplacer(
		"/", "-", "\\", "-", ":", "-", "*", "-",
		"?", "", "\"", "", "<", "", ">", "", "|", "-",
	)
	return strings.TrimSpace(replacer.Replace(name))
}

// renameToTitle renames the file to match the title tag if they differ.
func renameToTitle(filePath, title string) {
	if title == "" {
		return
	}
	dir := filepath.Dir(filePath)
	newName := sanitizeFilename(title) + ".mp3"
	newPath := filepath.Join(dir, newName)
	if filepath.Base(filePath) == newName {
		return
	}
	if err := os.Rename(filePath, newPath); err != nil {
		fmt.Println("Warning: could not rename file:", err)
		return
	}
	fmt.Printf("Renamed file to: %s\n", newName)
}

// ---- Single file mode ----

func fileMode(filePath string) {
	mp3File, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		log.Fatal("Error opening mp3 file: ", err)
	}
	if mp3File == nil {
		log.Fatal("File doesn't exist or is empty")
	}
	defer mp3File.Close()

	viewTags(mp3File)

	for {
		fmt.Println("0: Save & Quit")
		fmt.Println("1: Edit Title")
		fmt.Println("2: Edit Artist")
		fmt.Println("3: Edit Album")
		fmt.Println("4: Edit Cover Art")
		fmt.Println("5: View Tags")
		fmt.Println("")
		fmt.Print("Select an option: ")

		line, _ := stdin.ReadString('\n')
		option, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			fmt.Println("Invalid option")
			continue
		}

		switch option {
		case 0:
			if err = mp3File.Save(); err != nil {
				log.Fatal("Error saving changes to mp3: ", err)
			}
			renameToTitle(filePath, mp3File.Title())
			return
		case 1:
			mp3File.SetTitle(promptTitle())
		case 2:
			mp3File.SetArtist(promptArtist())
		case 3:
			mp3File.SetAlbum(promptAlbum())
		case 4:
			applyCoverArt(mp3File, promptCoverArt())
		case 5:
			viewTags(mp3File)
		default:
			fmt.Println("That option is not listed. Try again.")
		}
	}
}

// ---- Folder mode ----

func folderMode(folder string) {
	for {
		fmt.Println("0: Quit")
		fmt.Println("1: Edit Artist  (all files)")
		fmt.Println("2: Edit Album   (all files)")
		fmt.Println("3: Edit Cover Art (all files)")
		fmt.Println("")
		fmt.Print("Select an option: ")

		line, _ := stdin.ReadString('\n')
		option, err := strconv.Atoi(strings.TrimSpace(line))
		if err != nil {
			fmt.Println("Invalid option")
			continue
		}

		switch option {
		case 0:
			return
		case 1:
			artist := promptArtist()
			applyToFolder(folder, func(f *id3v2.Tag) { f.SetArtist(artist) })
		case 2:
			album := promptAlbum()
			applyToFolder(folder, func(f *id3v2.Tag) { f.SetAlbum(album) })
		case 3:
			imgPath := promptCoverArt()
			applyToFolder(folder, func(f *id3v2.Tag) { applyCoverArt(f, imgPath) })
		default:
			fmt.Println("That option is not listed. Try again.")
		}
	}
}

func applyToFolder(folder string, action func(*id3v2.Tag)) {
	err := filepath.Walk(folder, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".mp3") {
			fmt.Printf("Processing: %s\n", path)
			mp3File, err := id3v2.Open(path, id3v2.Options{Parse: true})
			if err != nil {
				log.Printf("Error opening %s: %v", path, err)
				return nil
			}
			action(mp3File)
			if err = mp3File.Save(); err != nil {
				log.Printf("Error saving %s: %v", path, err)
			}
			mp3File.Close()
		}
		return nil
	})
	if err != nil {
		log.Fatalf("Error reading folder: %v", err)
	}
}

// ---- Prompts ----

func viewTags(file *id3v2.Tag) {
	fmt.Println("Title: ", file.Title())
	fmt.Println("Artist: ", file.Artist())
	fmt.Println("Album: ", file.Album())
	fmt.Println("")
}

func promptTitle() string {
	fmt.Print("Title: ")
	val, err := stdin.ReadString('\n')
	val = strings.TrimSpace(val)
	if err != nil || val == "" {
		fmt.Println("Title cannot be empty!")
		return promptTitle()
	}
	return val
}

func promptArtist() string {
	return promptFromList("artist", lists.Artists, nil)
}

func promptAlbum() string {
	return promptFromList("album", lists.Albums, nil)
}

func promptCoverArt() string {
	covers := listCovers()
	dir := coversDir()

	validate := func(path string) bool {
		if _, err := os.Stat(path); err != nil {
			fmt.Println("File doesn't exist or can't be opened:", err)
			return false
		}
		return true
	}

	if len(covers) > 0 {
		fmt.Printf("Covers (%s):\n", dir)
		for i, name := range covers {
			fmt.Printf("  %d: %s\n", i+1, name)
		}
		fmt.Print("Enter number to select, or type a full path: ")

		input, err := stdin.ReadString('\n')
		input = strings.TrimSpace(input)
		input = strings.Trim(input, "'")

		if err != nil || input == "" {
			fmt.Println("Cover art cannot be empty!")
			return promptCoverArt()
		}

		if n, err2 := strconv.Atoi(input); err2 == nil {
			if n >= 1 && n <= len(covers) {
				return filepath.Join(dir, covers[n-1])
			}
			fmt.Printf("Invalid selection: choose 1-%d or type a full path\n", len(covers))
			return promptCoverArt()
		}

		if !validate(input) {
			return promptCoverArt()
		}
		return input
	}

	// No covers folder or empty — fall back to typing a path
	return promptFromList("cover art path", nil, validate)
}

// ---- Cover art ----

func applyCoverArt(file *id3v2.Tag, imgFilePath string) {
	openedImg, err := os.Open(imgFilePath)
	if err != nil {
		log.Fatal("Error opening image file: ", err)
	}
	defer openedImg.Close()

	imgFile, err := imaging.Decode(openedImg)
	if err != nil {
		fmt.Println("Error decoding image:", err)
		return
	}

	bounds := imgFile.Bounds()
	if bounds.Dx() != 300 || bounds.Dy() != 300 {
		imgFile = imaging.Resize(imgFile, 300, 300, imaging.Lanczos)
	}

	tmpFile, err := os.CreateTemp("", "coverArt-*.png")
	if err != nil {
		fmt.Println("Error creating temp file:", err)
		return
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	defer os.Remove(tmpPath)

	if err := imaging.Save(imgFile, tmpPath); err != nil {
		fmt.Println("Error saving resized image:", err)
		return
	}

	coverImg, err := os.ReadFile(tmpPath)
	if err != nil {
		fmt.Println("Error reading artwork file:", err)
		return
	}

	coverArt := id3v2.PictureFrame{
		Encoding:    id3v2.EncodingUTF8,
		MimeType:    "image/png",
		PictureType: id3v2.PTFrontCover,
		Description: "Front cover",
		Picture:     coverImg,
	}

	file.DeleteFrames(file.CommonID("Attached picture"))
	file.AddAttachedPicture(coverArt)
	fmt.Println("Updated cover art")
}
