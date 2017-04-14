// embed.go - prepare template files for embedding into the binary
//
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

// +build ignore

// This program is used to generate the "template_embed.go" file when
// "go generate" is called.
package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
)

var defaultOutName = flag.String("output", "", "output file name")

var out *os.File

func visit(path string, f os.FileInfo, err error) error {
	if err != nil {
		return err
	}
	if !f.Mode().IsRegular() {
		return nil
	}

	body, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "\t%q: %q,\n", filepath.ToSlash(path), body)
	return nil
}

func main() {
	flag.Parse()

	root := flag.Arg(0)

	var err error
	outName := *defaultOutName
	if outName == "" {
		outName = "template_embed.go"
	}
	out, err = os.Create(outName)
	if err != nil {
		log.Fatal(err)
	}
	defer out.Close()

	err = os.Chdir(root)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Fprint(out, `// automatically generated by embed.go

package epub

var templateFiles = map[string]string {
`)
	err = filepath.Walk(".", visit)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Fprint(out, `}
`)
}
