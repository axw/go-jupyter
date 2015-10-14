package main

import (
	"fmt"
	"log"
	"os"

	"github.com/axw/jupyter"
)

const (
	llgoJupyterVersion = "0.1.0"
)

type llgoKernel struct {
}

func (k *llgoKernel) Info() jupyter.KernelInfo {
	return jupyter.KernelInfo{
		Implementation:        "llgo-jupyter",
		ImplementationVersion: llgoJupyterVersion,
		LanguageInfo: jupyter.LanguageInfo{
			Name:          "go",
			Version:       "1.4.2",
			MimeType:      "text/x-go",
			FileExtension: ".go",
		},
		//Banner:                llgoJupyterBanner,
		//HelpLinks:             llgoJupyterHelpLinks,
	}
}

func (k *llgoKernel) Shutdown(restart bool) error {
	return nil
}

func (k *llgoKernel) Execute(code string, options jupyter.ExecuteOptions) (interface{}, error) {
	return fmt.Sprintf("resultOf(%s)", code), nil
}

func main() {
	connInfo, err := jupyter.ReadConnectionFile(os.Args[1])
	if err != nil {
		log.Fatalf("reading connection file: %v", err)
	}
	k := &llgoKernel{}
	if err := jupyter.RunKernel(k, connInfo); err != nil {
		log.Fatalf("running kernel: %v", err)
	}
}
