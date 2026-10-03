//go:build windows

package service

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestTaskXMLIsWellFormedAndEscaped(t *testing.T) {
	text := TaskXML(`DOMAIN\a&b`, `C:\Tools & Co\bin\tidalbridge-service.exe`, `C:\Users\a<b\.tidalbridge`)
	var parsed struct {
		Triggers struct {
			Logon struct {
				UserID string `xml:"UserId"`
			} `xml:"LogonTrigger"`
		} `xml:"Triggers"`
		Actions struct {
			Exec struct {
				Command   string `xml:"Command"`
				Arguments string `xml:"Arguments"`
			} `xml:"Exec"`
		} `xml:"Actions"`
		Settings struct {
			Limit string `xml:"ExecutionTimeLimit"`
		} `xml:"Settings"`
	}
	decoder := xml.NewDecoder(strings.NewReader(strings.Replace(text, `encoding="UTF-16"`, `encoding="UTF-8"`, 1)))
	if err := decoder.Decode(&parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Triggers.Logon.UserID != `DOMAIN\a&b` || parsed.Actions.Exec.Command != `C:\Tools & Co\bin\tidalbridge-service.exe` {
		t.Fatal("values not preserved", parsed)
	}
	if parsed.Actions.Exec.Arguments != `--data-dir "C:\Users\a<b\.tidalbridge"` || parsed.Settings.Limit != "PT0S" {
		t.Fatal("arguments/settings", parsed)
	}
}
