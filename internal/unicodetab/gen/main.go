// Command gen writes tables.go from testdata/oracle/unicode. Run it with go generate in internal/unicodetab.
package main

import (
	"flag"
	"log"
	"os"

	"github.com/pwngh/gtm-connector/internal/unicodetab/tablegen"
)

func main() {
	oracle := flag.String("oracle", "../../testdata/oracle", "the oracle folder")
	out := flag.String("out", "tables.go", "the file to write")
	flag.Parse()
	source, err := tablegen.Generate(*oracle)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*out, source, 0o644); err != nil {
		log.Fatal(err)
	}
}
