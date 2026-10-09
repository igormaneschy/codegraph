package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/Lordymine/codegraph/internal/bench"
)

// The opt-in matrix supplies one bounded request via stdin. Stdout stays JSON;
// no production cache or subprocess setup time is included in RunAtomic wall time.
type benchmarkWorkerRequest struct {
	Operation string `json:"operation"`
	bench.SampleRequest
	Edit *bench.MatrixEdit `json:"edit,omitempty"`
}

func cmdBenchSample(input io.Reader, output io.Writer) error {
	request, err := decodeSampleRequest(input)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if request.Operation == "edit" {
		if request.Edit == nil {
			return fmt.Errorf("edit operation needs edit input")
		}
		evidence, editErr := bench.EditMatrixInput(*request.Edit)
		return errors.Join(editErr, json.NewEncoder(output).Encode(evidence))
	}
	if request.Operation != "sample" {
		return fmt.Errorf("invalid benchmark operation %q: want sample or edit", request.Operation)
	}
	sample, runErr := bench.Sample(ctx, request.SampleRequest)
	return errors.Join(runErr, json.NewEncoder(output).Encode(sample))
}

func decodeSampleRequest(input io.Reader) (benchmarkWorkerRequest, error) {
	content, err := io.ReadAll(io.LimitReader(input, (4<<20)+1))
	if err != nil || len(content) > 4<<20 {
		return benchmarkWorkerRequest{}, fmt.Errorf("sample request exceeds 4 MiB or cannot be read: %v", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var request benchmarkWorkerRequest
	if err := decoder.Decode(&request); err != nil {
		return request, fmt.Errorf("invalid benchmark sample request: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return request, fmt.Errorf("sample request must contain exactly one JSON object")
	}
	return request, nil
}
