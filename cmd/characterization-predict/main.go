package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/anchorshell/relay/pkg/characterization"
)

func main() {
	actionPath := flag.String("action", "", "AnchorShell action bundle")
	metadataPath := flag.String("metadata", "", "AnchorShell metadata bundle")
	flag.Parse()
	if *actionPath == "" || *metadataPath == "" {
		fmt.Fprintln(os.Stderr, "-action and -metadata are required")
		os.Exit(2)
	}
	action, err := os.ReadFile(*actionPath)
	if err != nil {
		fatal(err)
	}
	metadata, err := os.ReadFile(*metadataPath)
	if err != nil {
		fatal(err)
	}
	classifier, err := characterization.NewFastTextClassifier(action, metadata)
	if err != nil {
		fatal(err)
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		prediction, err := classifier.Predict(context.Background(), characterization.CandidateInput{Text: append([]byte(nil), scanner.Bytes()...), SourceWeight: 1})
		if err != nil {
			fatal(err)
		}
		if err := encoder.Encode(prediction); err != nil {
			fatal(err)
		}
	}
	if err := scanner.Err(); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
