package geoipasset

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

var domainCodePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
var domainNamePattern = regexp.MustCompile(`^sb-geosite-([a-z][a-z0-9-]{0,63})-([a-f0-9]{64})\.dat$`)

// PublishDomains stores one immutable GeoSite database. References let both
// runtimes share matchers through Xray's existing weak GeoSite cache.
func PublishDomains(root, code string, expressions []string) (string, error) {
	if !domainCodePattern.MatchString(code) || len(expressions) == 0 || len(expressions) > 250000 {
		return "", errors.New("invalid GeoSite pack")
	}
	site := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), strings.ToUpper(code))
	for _, expression := range expressions {
		kind, value, ok := strings.Cut(expression, ":")
		if !ok || value == "" || len(value) > 1024 || !utf8.ValidString(value) {
			return "", errors.New("invalid GeoSite expression")
		}
		var typ uint64
		switch kind {
		case "keyword":
			typ = 0
		case "regexp":
			typ = 1
			if _, err := regexp.Compile(value); err != nil {
				return "", err
			}
		case "domain":
			typ = 2
		case "full":
			typ = 3
		default:
			return "", errors.New("unsupported GeoSite expression")
		}
		domain := protowire.AppendVarint(protowire.AppendTag(nil, 1, protowire.VarintType), typ)
		domain = protowire.AppendString(protowire.AppendTag(domain, 2, protowire.BytesType), value)
		site = protowire.AppendBytes(protowire.AppendTag(site, 2, protowire.BytesType), domain)
	}
	data := protowire.AppendBytes(protowire.AppendTag(nil, 1, protowire.BytesType), site)
	if len(data) > 8<<20 {
		return "", errors.New("GeoSite pack is oversized")
	}
	return publishEncoded(root, "geosite", code, data)
}

func DomainReferenceName(reference string) (string, bool) {
	parts := strings.Split(reference, ":")
	if len(parts) != 3 || parts[0] != "ext" {
		return "", false
	}
	match := domainNamePattern.FindStringSubmatch(parts[1])
	return parts[1], match != nil && match[1] == parts[2]
}
func managedReferenceName(reference string) (string, bool) {
	if name, ok := ReferenceName(reference); ok {
		return name, true
	}
	return DomainReferenceName(reference)
}

// VerifyDomain rejects arbitrary paths and verifies content-addressed assets
// without retaining a second in-memory database.
func VerifyDomain(root, reference string) error {
	name, ok := DomainReferenceName(reference)
	if !ok {
		return errors.New("invalid GeoSite reference")
	}
	path := filepath.Join(root, name)
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > 8<<20 {
		return errors.New("unsafe GeoSite asset")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return errors.New("GeoSite asset changed during open")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, (8<<20)+1))
	if err != nil {
		return err
	}
	match := domainNamePattern.FindStringSubmatch(name)
	if n > 8<<20 || hex.EncodeToString(hash.Sum(nil)) != match[2] {
		return errors.New("GeoSite asset checksum mismatch")
	}
	return nil
}
