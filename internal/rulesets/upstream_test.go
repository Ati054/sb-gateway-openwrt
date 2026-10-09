package rulesets

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestCompileDomainListIncludesAndKinds(t *testing.T) {
	files := map[string]string{
		"category-ru": "# comment\ninclude:bank-ru\nexample.ru\nfull:exact.ru @ads\nkeyword:bank\nregexp:^cdn[0-9]+\\.ru$\n",
		"bank-ru":     "domain:bank.ru\ninclude:category-ru\n",
	}
	fetch := func(_ context.Context, name string) (string, error) {
		value, ok := files[name]
		if !ok {
			return "", errors.New("missing source")
		}
		return value, nil
	}
	rules, err := Compile(context.Background(), "category-ru", fetch)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rules.Exact, []string{"exact.ru"}) ||
		!reflect.DeepEqual(rules.Suffix, []string{"bank.ru", "example.ru"}) ||
		!reflect.DeepEqual(rules.Keyword, []string{"bank"}) ||
		!reflect.DeepEqual(rules.Regex, []string{`^cdn[0-9]+\.ru$`}) {
		t.Fatalf("unexpected rules: %+v", rules)
	}
}

func TestCompileRejectsUnsafeOrIncompleteSource(t *testing.T) {
	for _, source := range []string{"include:../secrets", "regexp:[", "full:bad..ru", "unknown:x"} {
		fetch := func(context.Context, string) (string, error) { return source, nil }
		if _, err := Compile(context.Background(), "category-ru", fetch); err == nil {
			t.Errorf("accepted %q", source)
		}
	}
	if _, err := Compile(context.Background(), "../bad", func(context.Context, string) (string, error) {
		return "ok.ru", nil
	}); err == nil {
		t.Fatal("accepted unsafe source name")
	}
}
