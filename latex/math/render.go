// render.go -
// Copyright (C) 2016  Jochen Voss <voss@seehuhn.de>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package math

import (
	"encoding/hex"
	"flag"
	"fmt"
	"html"
	"image"
	"image/draw"
	"image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/seehuhn/epublatex/epub"
	"github.com/seehuhn/epublatex/latex/render"
	"golang.org/x/crypto/sha3"
)

var cacheDir = flag.String("latex-math-cache", "",
	"cache directory for maths rendering")

var noCache = flag.Bool("latex-math-no-cache", false,
	"whether to disable the rendering cache")

const (
	renderRes = 3 * 96
	imgNames  = "img%d.png"
	xHeight   = 4.30554 // x-height of cmi10 in TeX pt
)

type Renderer struct {
	book     epub.Writer
	preamble []string
	formulas map[string]int

	cacheDir string
}

func NewRenderer(book epub.Writer) (*Renderer, error) {
	r := &Renderer{
		book:     book,
		formulas: make(map[string]int),
	}

	cacheDir := *cacheDir
	if len(cacheDir) == 0 {
		cacheDir = os.Getenv("JV_EBOOK_CACHE")
	}
	if len(cacheDir) == 0 {
		cacheDir = os.ExpandEnv(defaultCacheDir)
		cacheDir = filepath.Join(cacheDir, "de.seehuhn.ebook")
	}

	r.cacheDir = filepath.Join(cacheDir, "maths")
	err := os.MkdirAll(r.cacheDir, 0755)
	if err != nil {
		return nil, err
	}

	return r, nil
}

func (r *Renderer) AddPreamble(line string) {
	r.preamble = append(r.preamble, line)
}

func (r *Renderer) AddFormula(env, formula string) {
	if strings.Contains(env, "%") {
		panic("invalid math environment " + env)
	}
	key := env + "%" + formula
	r.formulas[key]++
}

func (r *Renderer) Finish() (res Images, err error) {
	res = make(Images)
	if len(r.formulas) == 0 {
		return res, nil
	}

	all := r.getFormulaInfo()

	needed := 0
	for _, info := range all {
		if info.Needed {
			needed++
		}
	}
	var c <-chan image.Image
	if needed > 0 {
		q, err := render.NewQueue(renderRes)
		if err != nil {
			return nil, err
		}
		defer func() {
			e2 := q.Finish()
			if err == nil {
				err = e2
			}
		}()

		tmpl, err := template.New("tex").Parse(texTemplate)
		if err != nil {
			return nil, err
		}
		data := map[string]interface{}{
			"Preamble": r.preamble,
			"Formulas": all,
		}
		c = q.Submit(tmpl, data)
	}
	err = r.gatherImages(res, all, c)
	if err != nil {
		return nil, err
	}

	return res, nil
}

func (r *Renderer) getFormulaInfo() []*formulaInfo {
	var all []*formulaInfo
	for key, count := range r.formulas {
		parts := strings.SplitN(key, "%", 2)
		info := &formulaInfo{
			Key:     key,
			Env:     parts[0],
			Formula: parts[1],
			Count:   count,
			Needed:  !r.isCached(key),
		}
		all = append(all, info)
	}
	sort.Sort(decreasingCount(all))
	for i, info := range all {
		info.FileName = strconv.Itoa(i)
	}
	return all
}

const texTemplate = `\documentclass{minimal}

\usepackage[paperwidth=6in,paperheight=9in,margin=0pt]{geometry}

\usepackage{pdfrender}
\pdfrender{TextRenderingMode=2,LineWidth=0.05pt}

{{range .Preamble -}}
{{.}}
{{end}}
\parindent0pt
\parskip0pt

\begin{document}
\fontsize{10}{12}\selectfont

{{range .Formulas -}}
{{if .Needed -}}
{{if eq .Env "$" -}}
\vrule width6bp height4.3pt depth0pt \kern6bp
${{.Formula}}$
{{else -}}
\begin{ {{- .Env -}} }
  {{.Formula}}
\end{ {{- .Env -}} }
{{end -}}
\newpage

{{end -}}
{{end -}}
\end{document}
`

type imageWriter func(io.Writer) error

func (r *Renderer) addImage(name, mime string, fn imageWriter) (string, error) {
	file := r.book.RegisterFile("m/"+name, mime, false)
	w, err := r.book.CreateFile(file)
	if err != nil {
		return "", err
	}
	err = fn(w)
	if err != nil {
		return "", err
	}
	return file.Path, nil
}

func (r *Renderer) gatherImages(
	res map[string]string, all []*formulaInfo, c <-chan image.Image) error {
	for _, info := range all {
		var crop func(imgIn image.Image) image.Image
		var cssClass string
		if info.Env == "$" {
			crop = cropInline
			cssClass = "imath"
		} else {
			crop = cropDisplayed
			cssClass = "dmath"
		}

		var img image.Image
		var err error
		if info.Needed {
			img = <-c
			if img == nil {
				log.Fatal("missing image")
			}
			img = crop(img)
			err = r.writeCached(info.Key, img)
			if err != nil {
				return err
			}
		} else {
			img, err = r.loadCached(info.Key)
			if err != nil {
				return err
			}
		}

		fileName, err := r.addImage("m/"+info.FileName, "image/png",
			func(w io.Writer) error { return png.Encode(w, img) })
		if err != nil {
			return err
		}

		exWidth := float64(img.Bounds().Dx()) / float64(renderRes) * 72.27 / xHeight
		s := fmt.Sprintf(
			`<img alt="%s" src="%s" class="%s" style="width: %.2fex"/>`,
			html.EscapeString(info.Formula), html.EscapeString(fileName),
			cssClass, exWidth)
		res[info.Key] = s
	}
	return nil
}

func cropInline(imgIn image.Image) image.Image {
	b := imgIn.Bounds()
	imgOut := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(imgOut, imgOut.Bounds(), imgIn, b.Min, draw.Src)

	// find the marker on the left
	y0 := 0
	for {
		if imgOut.Pix[y0*imgOut.Stride+3] != 0 {
			break
		}
		y0++
	}
	y1 := y0
	for {
		if imgOut.Pix[(y1+1)*imgOut.Stride+3] == 0 {
			break
		}
		y1++
	}
	yMid := (y0 + y1) / 2

	// find the width of the marker
	xMin := 0
	for imgOut.Pix[imgOut.PixOffset(xMin, yMid)+3] != 0 {
		xMin++
	}

	// find the top-most row of pixels used
	idx := 0
	for imgOut.Pix[idx+3] == 0 {
		idx += 4
	}
	yMin := idx / imgOut.Stride

	// find the bottom-most row of pixels used
	idx = imgOut.Rect.Max.Y*imgOut.Stride - 4
	for imgOut.Pix[idx+3] == 0 {
		idx -= 4
	}
	yMax := idx/imgOut.Stride + 1

	// Centre the crop window vertically.
	if y0-yMin > yMax-1-y1 {
		yMax = y0 + y1 - yMin + 1
	} else {
		yMin = y0 + y1 - yMax + 1
	}

	// crop left
leftLoop:
	for xMin < imgOut.Rect.Max.X {
		for y := yMin; y < yMax; y++ {
			idx := imgOut.PixOffset(xMin, y)
			if imgOut.Pix[idx+3] != 0 {
				break leftLoop
			}
		}
		xMin++
	}

	// crop right
	xMax := imgOut.Rect.Max.X
rightLoop:
	for xMax > xMin {
		for y := yMin; y < yMax; y++ {
			idx := imgOut.PixOffset(xMax-1, y)
			if imgOut.Pix[idx+3] != 0 {
				break rightLoop
			}
		}
		xMax--
	}

	crop := image.Rectangle{
		Min: image.Point{xMin, yMin},
		Max: image.Point{xMax, yMax},
	}
	return imgOut.SubImage(crop)
}

func cropDisplayed(imgIn image.Image) image.Image {
	b := imgIn.Bounds()
	imgOut := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(imgOut, imgOut.Bounds(), imgIn, b.Min, draw.Src)

	// find the top-most row of pixels used
	idx := 0
	for imgOut.Pix[idx+3] == 0 {
		idx += 4
	}
	yMin := idx / imgOut.Stride

	// find the bottom-most row of pixels used
	idx = imgOut.Rect.Max.Y*imgOut.Stride - 4
	for imgOut.Pix[idx+3] == 0 {
		idx -= 4
	}
	yMax := idx/imgOut.Stride + 1

	// crop left and right
	xMin := 0
	xMax := imgOut.Rect.Max.X
leftRightLoop:
	for xMin < imgOut.Rect.Max.X {
		for y := yMin; y < yMax; y++ {
			idx := imgOut.PixOffset(xMin, y)
			if imgOut.Pix[idx+3] != 0 {
				break leftRightLoop
			}
		}
		for y := yMin; y < yMax; y++ {
			idx := imgOut.PixOffset(xMax-1, y)
			if imgOut.Pix[idx+3] != 0 {
				break leftRightLoop
			}
		}
		xMin++
		xMax--
	}

	crop := image.Rectangle{
		Min: image.Point{xMin, yMin},
		Max: image.Point{xMax, yMax},
	}
	return imgOut.SubImage(crop)
}

func (r *Renderer) cacheFileName(key string) string {
	h := sha3.NewShake128()
	h.Write([]byte(strconv.Itoa(renderRes) + "%" + key))
	buf := make([]byte, 16)
	h.Read(buf)
	fileName := hex.EncodeToString(buf) + ".png"
	return filepath.Join(r.cacheDir, fileName)
}

func (r *Renderer) isCached(key string) bool {
	if *noCache {
		return false
	}
	_, err := os.Stat(r.cacheFileName(key))
	return err == nil
}

func (r *Renderer) writeCached(key string, img image.Image) error {
	fd, err := os.Create(r.cacheFileName(key))
	if err != nil {
		return err
	}
	defer fd.Close()

	return png.Encode(fd, img)
}

func (r *Renderer) loadCached(key string) (image.Image, error) {
	fd, err := os.Open(r.cacheFileName(key))
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	img, _, err := image.Decode(fd)
	if err != nil {
		return nil, err
	}
	return img, nil
}

type formulaInfo struct {
	Key string

	Env     string
	Formula string
	Count   int

	Needed   bool
	FileName string
}

type decreasingCount []*formulaInfo

func (dc decreasingCount) Len() int      { return len(dc) }
func (dc decreasingCount) Swap(i, j int) { dc[i], dc[j] = dc[j], dc[i] }
func (dc decreasingCount) Less(i, j int) bool {
	if dc[i].Count != dc[j].Count {
		return dc[i].Count > dc[j].Count
	}
	if dc[i].Formula != dc[j].Formula {
		return dc[i].Formula < dc[j].Formula
	}
	return dc[i].Env < dc[j].Env
}
