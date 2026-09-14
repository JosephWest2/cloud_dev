package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
)

const downConfirmationInputLimit = 1024

// newDownConfirmation previews only the already frozen candidates. Approval
// never selects resources; the lifecycle service revalidates those exact IDs.
func newDownConfirmation(input io.Reader, output io.Writer, interactive func() bool) lifecycle.ConfirmDown {
	return func(ctx context.Context, scope config.Config, candidates []lifecycle.Instance, yes bool) (bool, error) {
		var preview strings.Builder
		fmt.Fprintf(&preview, "devbox: down --all preview account=%q region=%q deployment=%q owner=%q count=%d\n", scope.ExpectedAccount, scope.Region, scope.Deployment, scope.Owner, len(candidates))
		for _, candidate := range candidates {
			fmt.Fprintf(&preview, "  instance_id=%q name=%q group=%q\n", candidate.ID, candidate.Name, candidate.Group)
		}
		if err := writeDownConfirmation(output, preview.String()); err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if yes {
			return true, nil
		}
		isInteractive := interactive != nil && interactive()
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if !isInteractive || input == nil {
			return false, downConfirmationRequired()
		}
		if err := writeDownConfirmation(output, fmt.Sprintf("Terminate these %d instances? [y/N] ", len(candidates))); err != nil {
			return false, err
		}
		if err := ctx.Err(); err != nil {
			return false, err
		}

		type response struct {
			line string
			err  error
		}
		answer := make(chan response, 1)
		go func() {
			// The byte cap also bounds character count. A line must include its
			// newline within the cap; EOF never supplies affirmative consent.
			line, err := bufio.NewReader(io.LimitReader(input, downConfirmationInputLimit)).ReadString('\n')
			// Input belongs to the caller. Cancellation must not close stdin;
			// the buffered send can finish if that read completes after return.
			answer <- response{line: line, err: err}
		}()
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case response := <-answer:
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if response.err != nil {
				return false, downConfirmationRequired()
			}
			choice := strings.ToLower(strings.TrimSpace(response.line))
			return choice == "y" || choice == "yes", nil
		}
	}
}

func writeDownConfirmation(output io.Writer, message string) error {
	if output != nil {
		n, err := io.WriteString(output, message)
		if err == nil && n == len(message) {
			return nil
		}
	}
	return &lifecycle.Failure{Code: "output_unavailable", Message: "cannot write the complete teardown preview or confirmation prompt; no termination approved"}
}

func downConfirmationRequired() error {
	return &lifecycle.Failure{Code: "confirmation_required", Message: "down --all requires a complete y/yes response from a terminal; use --yes for noninteractive confirmation"}
}
