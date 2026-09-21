package cli

import (
	"fmt"
	"io"

	"github.com/ndianabasi/swe-cache-manager/internal/config"
	"github.com/ndianabasi/swe-cache-manager/internal/diagnostic"
)

type statusRow struct {
	label string
	value string
}

// renderStatus deliberately uses plain ASCII so the output remains readable in
// redirected logs and default Windows terminals, without a rendering library.
func renderStatus(out io.Writer, c config.Config, report diagnostic.Report) {
	fmt.Fprintln(out, "swe-cache status")
	fmt.Fprintln(out, "================")
	renderStatusSection(out, "SERVICES", []statusRow{
		{"Container", report.Container},
		{"APT cache", report.APT},
		{"OCI registry", report.OCI},
	})
	renderStatusSection(out, "ENDPOINTS", []statusRow{
		{"APT proxy", fmt.Sprintf("http://127.0.0.1:%d", c.APT.Port)},
		{"OCI registry", fmt.Sprintf("http://127.0.0.1:%d", c.OCI.Port)},
	})
	renderStatusSection(out, "PERSISTENT STORAGE", []statusRow{
		{"Cache root", c.Root},
		{"APT cache", report.Paths["apt"]},
		{"OCI cache", report.Paths["oci"]},
		{"Git mirrors", report.Paths["git"]},
	})
	renderStatusSection(out, "RUNTIME", []statusRow{
		{"CLI version", Version},
		{"Service image", c.Image},
	})
}

func renderStatusSection(out io.Writer, title string, rows []statusRow) {
	width := 0
	for _, row := range rows {
		if len(row.label) > width {
			width = len(row.label)
		}
	}
	fmt.Fprintf(out, "\n%s\n", title)
	for _, row := range rows {
		fmt.Fprintf(out, "  %-*s  %s\n", width, row.label, row.value)
	}
}
