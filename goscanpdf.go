package main

import (
	"bufio"
	"flag"
	_ "github.com/spakin/netpbm"
	"image"
	"log"
	"math"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var tempDir string

// Cleanup function instead of logger.Fatalln
func cleanExit(message string, exitcode ExitCode) {

	if strings.HasPrefix(tempDir, "/dev/shm/") { // ensure that we only delete stuff in the ramdisk
		os.RemoveAll(tempDir)
	}

	if message != "" {
		log.Println(message)
	}

	// Write message to socket /tmp/goscanpdf.sock if exists. A display daemon might listen there.

	socketConnection, err := net.Dial("unix", "/tmp/goscanpdf.sock")
	if err == nil {
		socketConnection.Write([]byte(message))
		socketConnection.Close()
	}

	// flash LED according to exitcode

	_ = exec.Command("sh", "-c", "echo none > /sys/class/leds/led0/trigger").Run()

	err = exec.Command("sh", "-c", "echo 0 > /sys/class/leds/led0/brightness").Run()
	if err == nil {
		for i := byte(0); i < byte(exitcode); i++ {
			time.Sleep(300 * time.Millisecond)
			err = exec.Command("sh", "-c", "echo 255 > /sys/class/leds/led0/brightness").Run()
			time.Sleep(300 * time.Millisecond)
			err = exec.Command("sh", "-c", "echo 0 > /sys/class/leds/led0/brightness").Run()
		}
	}

	// exit with exitcode

	os.Exit(int(exitcode))
}

// returns the contents (without prefix) of the first line which starts with the given prefix
func getLine(lines string, prefix string) string {
	for _, line := range strings.Split(lines, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	return ""
}

func main() {

	log.SetPrefix("[goscanpdf] ")
	log.SetFlags(log.Ldate|log.Lmicroseconds)

	// Parse command line arguments. For non-existing or unparseable arguments, strconv.Atoi() returns zero and we use the default value instead.

	var dpi int
	var convertCores int
	var pdfPrefix string

	flag.IntVar(&dpi, "dpi", 200, "scan `resolution` in dots per inch")
	flag.IntVar(&convertCores, "cores", 3, "Maximum `number` of simultaneous single-core convert workers. You might want to spare one CPU core for scanimage, which typically produces about 50% of goscanpdf's CPU load but can't be parallelized.")
	flag.StringVar(&pdfPrefix, "prefix", "", "a `string` which is prepended to the PDF filename")
	flag.Parse()

	pdfPrefix = strings.Replace(pdfPrefix, "/", "", -1)

	if dpi < 72 {
		dpi = 72
	}

	if dpi > 600 {
		dpi = 600
	}

	if convertCores < 1 {
		convertCores = 1
	}

	if convertCores > 32 {
		convertCores = 32
	}

	log.Printf("Using %d dpi, %d convert workers and PDF prefix '%s'", dpi, convertCores, pdfPrefix)

	// --

	pages := []*page{}
	nextJob := make(chan *page)
	scannextpage := make(chan bool) // Alternative: Warten bis convertWorkers.Size < convertCores, aber dafür gibt es keine Methode.
	convertWorkers := &sync.WaitGroup{}

	// catch certain signals ("A SIGHUP, SIGINT, or SIGTERM signal causes the program to exit.")

	exitChan := make(chan os.Signal, 1)
	signal.Notify(exitChan, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-exitChan
		cleanExit("Caught exit signal, cleaning up", ExitSystemError)
	}()

	// Check that the ramdisk /dev/shm exists

	err := exec.Command("mountpoint", "-q", "/dev/shm").Run()
	if err != nil {
		cleanExit("/dev/shm is not mounted", ExitSystemError)
	}

	// create our temp folder (we need it for the rsync dry-run)

	mkTempOut, err := exec.Command("mktemp", "-d", "-p", "/dev/shm").Output()
	if err != nil {
		cleanExit("Error creating temporary folder", ExitSystemError)
	}

	tempDir = strings.TrimSpace(string(mkTempOut)) // mkTempOut has a trailing line break which we remove here

	log.Println("Using temporary folder " + tempDir)

	// Fail fast. Our goal is to not feed the scanner if there is an error. First check for the binaries.

	_, err = exec.LookPath("gm")
	if err != nil {
		cleanExit("Can't find graphicsmagick", ExitSystemError)
	}

	_, err = exec.LookPath("pdfunite")
	if err != nil {
		cleanExit("Can't find pdfunite", ExitSystemError)
	}

	// Test rsync connection. rsync --dry-run takes too long, so we extract "hostname" and "port" from the ssh config and use netcat.

	sshConfig, err := exec.Command("ssh", "-G", "goscanpdf-target").Output() // ssh -G prints the relevant configuration
	if err != nil {
		cleanExit("ssh -G goscanpdf-target failed", ExitNetworkError)
	}

	hostname := getLine(string(sshConfig), "hostname")
	port := getLine(string(sshConfig), "port")

	if hostname == "" || port == "" {
		cleanExit("ssh -G goscanpdf-target: hostname or port missing", ExitSystemError)
	}

	err = exec.Command("nc", "-w", "1", "-z", hostname, port).Run()
	if err != nil {
		cleanExit("nc failed: rsync dest "+hostname+":"+port+" not available", ExitNetworkError)
	}

	// Assemble options, get available device-specific options, prepare scanimage command. We don't use swdespeck and swdeskew because they are quite slow.

	options := []string{
		"--batch=" + tempDir + "/out%d.pnm",
		"--batch-prompt",
		"--batch-print",
	}

	availableAsBytes, err := exec.Command("scanimage", "-A").Output()
	if err != nil {
		cleanExit("Error getting available options. Is the scanner attached?", ExitNoScanner)
	}

	available := string(availableAsBytes)

	if strings.Contains(available, "--resolution ") {
		options = append(options, "--resolution="+strconv.Itoa(dpi))
	}

	if strings.Contains(available, "--mode ") {
		options = append(options, "--mode=Color")
	}

	if strings.Contains(available, "--page-width ") {
		options = append(options, "--page-width=221.121")
	}

	if strings.Contains(available, "--page-height ") {
		options = append(options, "--page-height=876.695")
	}

	if strings.Contains(available, "-l ") {
		options = append(options, "-l", "0")
	}

	if strings.Contains(available, "-t ") {
		options = append(options, "-t", "0")
	}

	if strings.Contains(available, "-x ") {
		options = append(options, "-x", "221.121")
	}

	if strings.Contains(available, "-y ") {
		options = append(options, "-y", "876.695")
	}

	if strings.Contains(available, "--ald") { // manpage says: "-ald[=(yes|no)] [no]"
		options = append(options, "--ald=yes")
	}

	if strings.Contains(available, "--overscan ") {
		options = append(options, "--overscan=On")
	}

	if strings.Contains(available, "--prepick ") {
		options = append(options, "--prepick=On")
	}

	if strings.Contains(available, "--source ") && strings.Contains(available, "ADF Duplex") {
		options = append(options, "--source", "ADF Duplex")
	} else {
		options = append(options, "--batch-count=1") // avoid infinite loop on Flatbed scanners
	}

	if strings.Contains(available, "--swcrop") { // manpage says: "--swcrop[=(yes|no)] [no]"
		options = append(options, "--swcrop=yes")
	}

	if strings.Contains(available, "--buffermode ") {
		options = append(options, "--buffermode=On") // buffer into the internal memory of the scanner
	}

	if strings.Contains(available, "--sleeptimer ") {
		options = append(options, "--sleeptimer=10") // minutes
	}

	if strings.Contains(available, "--brightness ") {
		options = append(options, "--brightness=9")
	}

	if strings.Contains(available, "--contrast ") {
		options = append(options, "--contrast=9")
	}

	scanimage := exec.Command("scanimage", options...)

	// scanimage: stdin waits for the <RETURN>

	scanimageIn, err := scanimage.StdinPipe()
	if err != nil {
		cleanExit("Error getting StdinPipe", ExitSystemError)
	}

	// scanimage writes "Press <RETURN> to continue" to stderr before, between and after the pages. This goroutine presses return accordingly.

	scanimageErr, err := scanimage.StderrPipe()
	if err != nil {
		cleanExit("Error getting StderrPipe", ExitSystemError)
	}

	go func() {

		scanimageErrScanner := bufio.NewScanner(scanimageErr)

		for scanimageErrScanner.Scan() { // Scan() is blocking

			log.Println(scanimageErrScanner.Text())

			if strings.Contains(scanimageErrScanner.Text(), "no SANE devices found") {
				cleanExit("No SANE devices found", ExitNoScanner)
			}

			if strings.Contains(scanimageErrScanner.Text(), "Press <RETURN> to continue") {

				// sleep a bit until scanimage is ready for the <RETURN>
				time.Sleep(100 * time.Millisecond)

				// press <RETURN>
				scanimageIn.Write([]byte("\n")) // \x04 = Ctrl+D = Cancel doesn't work here

				// wait until ready for next page
				_ = <-scannextpage
			}
		}
	}()

	// scanimage prints the pnm filenames to stdout. These goroutines read the filenames and create the compressed pdf files.

	scanimageOut, err := scanimage.StdoutPipe()
	if err != nil {
		cleanExit("Error getting StdoutPipe", ExitSystemError)
	}

	// Read the pnm filenames from stdout. Package bufio ist not thread-safe, so this is a separate single goroutine for Scan().

	go func() {

		scanimageOutScanner := bufio.NewScanner(scanimageOut)

		for scanimageOutScanner.Scan() {

			page := &page{PnmName: scanimageOutScanner.Text(), Keep: true}

			// Collect the pdf pages right now to preserve their order.
			pages = append(pages, page)

			// Enqueue the pnm file for conversion to pdf. Blocks if all workers are busy (until a worker reads from nextJob).
			nextJob <- page

			// The current page is being processed by a worker now, so we scan the next page.
			scannextpage <- true
		}
	}()

	// Start the workers.

	for i := 0; i < convertCores; i++ {

		convertWorkers.Add(1)

		go func() {

			for page := range nextJob { // blocks until nextJob is closed

				// Count dark and bright pixels in the pnm file, using a threshold of 50%.
				// Takes 100 msec on a laptop with SSD.
				// We don't use scanimage --swskip because we can't set the threshold there.

				pnmfile, err := os.Open(page.PnmName)
				if err != nil {
					cleanExit("Error opening pnm file", ExitSystemError)
				}

				pnmimage, _, err := image.Decode(pnmfile)
				if err != nil {
					cleanExit("Error reading pnm file", ExitSystemError)
				}

				border := int(
					0.03 * math.Min(
						float64(pnmimage.Bounds().Max.X),
						float64(pnmimage.Bounds().Max.Y),
					),
				)

				var brightPixels float64
				var darkPixels float64

				for x := pnmimage.Bounds().Max.X - border; x > border; x-- {
					for y := pnmimage.Bounds().Max.Y - border; y > border; y-- {
						r, g, b, _ := pnmimage.At(x, y).RGBA()
						if (r+g+b)/3 > 32768 {
							brightPixels++
						} else {
							darkPixels++
						}
					}
				}

				pnmfile.Close()

				// Keep pnm images with a dark pixel ratio greater than 0.0003. (ecodms separator page on flatbed scanner: 0.0008)

				darkPixelRatio := darkPixels / (darkPixels + brightPixels)

				log.Println("Dark pixel ratio:", darkPixelRatio)

				if darkPixelRatio > 0.0003 {

					// convert pnm to pdf

					convert := exec.Command("gm", "convert", "-set", "units", "PixelsPerInch", "-density", strconv.Itoa(dpi), page.PnmName, "-compress", "jpeg", "-quality", "70", page.PdfName())
					convert.Stderr = os.Stderr

					err = convert.Run()
					if err != nil {
						cleanExit("Error converting pnm to pdf", ExitSystemError)
					}

				} else {
					page.Keep = false
				}

				os.Remove(page.PnmName)

				// Uncomment the Sleep instruction for testing.
				// With convertCores = 1: Program pauses between creating single-page pdf files.
				// With convertCores = 3: Program creates three single-page pdf files without a pause.
				// (Scan speed is not affected immediately because of --buffermode=On)
				//
				// time.Sleep(20 * time.Second)
			}

			convertWorkers.Done()
		}()
	}

	// start scanimage

	err = scanimage.Start()
	if err != nil {
		cleanExit("Error starting scanimage", ExitSystemError)
	}

	// wait for termination

	scanimage.Wait()
	close(nextJob)
	convertWorkers.Wait()

	// cancel if zero pages are left

	pdfpages := []string{}

	for _, page := range pages {
		if page.Keep {
			pdfpages = append(pdfpages, page.PdfName())
		}
	}

	if len(pdfpages) == 0 {
		cleanExit("Zero pages scanned, cancelling", ExitZeroPages)
	}

	// create output filename

	outputFilename := tempDir + "/" + pdfPrefix + time.Now().Format("2006-01-02-15-04-05.000000") + ".pdf"

	// create output PDF file

	pdfunite := exec.Command("pdfunite", append(pdfpages, outputFilename)...)
	pdfunite.Stderr = os.Stderr

	err = pdfunite.Run()
	if err != nil {
		cleanExit("Error converting to pdf", ExitSystemError)
	}

	log.Println(len(pdfpages), "pages scanned to", outputFilename)

	// Delete temporary PDF files. Ignore if any of them fails.

	for _, pdfpage := range pdfpages {
		os.Remove(pdfpage)
	}

	// Three tries to rsync PDF file, use ssh batch mode (no password prompt).
	// Don't use --append because it implies --inplace which might confuse ecoDMS.

	for try := 0; try < 3; try++ {

		err = exec.Command("rsync", "-e", "ssh -o BatchMode=yes", outputFilename, "goscanpdf-target:"+pdfPrefix+"scaninput/").Run()
		if err == nil {
			break
		}
	}

	if err != nil {
		cleanExit("Error uploading: "+err.Error(), ExitNetworkError)
	}

	// cleanup and exit

	cleanExit("Done", ExitSuccess)
}
