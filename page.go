package main

type page struct {
	PnmName string
	Keep    bool
}

func (p *page) PdfName() string {
	return p.PnmName + ".pdf"
}
