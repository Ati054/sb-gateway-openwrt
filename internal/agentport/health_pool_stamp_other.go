//go:build !linux

package agentport

import (
	"fmt"
	"os"
	"reflect"
	"strings"
)

func healthPoolChangeStamp(info os.FileInfo) string {
	// Do not include access time: loading a catalogue is not a new generation.
	value := reflect.ValueOf(info.Sys())
	if value.Kind() == reflect.Pointer {
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return ""
	}
	parts := []string{}
	for _, name := range []string{"Dev", "Ino", "Ctim", "Ctimespec", "CreationTime", "LastWriteTime"} {
		field := value.FieldByName(name)
		if field.IsValid() && field.CanInterface() {
			parts = append(parts, fmt.Sprint(field.Interface()))
		}
	}
	return strings.Join(parts, ":")
}
