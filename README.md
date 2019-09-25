# goscanpdf

goscanpdf performs a batch scan with scanimage, converts the uncompressed PNM files to PDF files in the background and concatenates these PDF files to a single, timestamp-named PDF file. It has been tested with a Fujitsu ScanSnap iX500 on a Raspberry Pi 3 running Linux, but might work with other devices as well.

Pages with a dark pixel ratio less than 0.0003 are sorted out. The "dark" threshold for a pixel is 50%.

The resulting PDF file is rsync'ed to `goscanpdf-target:${prefix}scaninput`. There will be no password prompt, you must use key based authentication insted.

## Installation

Compile goscanpdf, e.g. for Linux on a RaspberryPi:

```
export GOOS=linux
export GOARCH=arm
go build path/to/goscanpdf.go
```

Adjust your .ssh/config:

```
Host goscanpdf-target
    HostName example.com
    User alice
```

In order to speed up scanimage, you can remove unused backends from `/etc/sane.d/dll.conf`.

You can use [insaned](https://github.com/abusenius/insaned) to run goscanpdf when a button on the scanner is pressed.

## System requirements

The next page is not scanned until the latest page has been passed to a worker thread. Thus there are up to `cores`+1 uncompressed PNM files in the working directory.

### Binaries

* `graphicsmagick`
* `netcat`
* `openssh`
* `pdfunite` (from poppler)
* `rsync`
* `scanimage` >= 1.0.25 (from sane-utils)
* `sh` (a shell)

## Exit values and RaspberryPi LED flash counts:

* 0 - output file successfully created
* 1 - system error
* 2 - network error
* 3 - no scanner found
* 4 - zero pages scanned
