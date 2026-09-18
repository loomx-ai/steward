package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestDetectLanguage(t *testing.T) {
	tests := []struct {
		name   string
		env    map[string]string
		system string
		want   language
	}{
		{"simplified Chinese", map[string]string{"LANG": "zh_CN.UTF-8"}, "", chinese},
		{"traditional Chinese", map[string]string{"LANG": "zh_TW.UTF-8"}, "", chinese},
		{"bare zh", map[string]string{"LANG": "zh"}, "", chinese},
		{"English", map[string]string{"LANG": "en_US.UTF-8"}, "", english},
		{"other language falls back to English", map[string]string{"LANG": "ja_JP.UTF-8"}, "", english},
		{"POSIX locale", map[string]string{"LANG": "C"}, "zh-CN", english},
		{"LC_ALL overrides LANG", map[string]string{"LC_ALL": "en_US.UTF-8", "LANG": "zh_CN.UTF-8"}, "", english},
		{"LC_MESSAGES overrides LANG", map[string]string{"LC_MESSAGES": "zh_CN.UTF-8", "LANG": "en_US.UTF-8"}, "", chinese},
		{"LANGUAGE list first entry", map[string]string{"LANGUAGE": "zh_CN:en", "LANG": "en_US.UTF-8"}, "", chinese},
		{"Windows Chinese locale", nil, "zh-CN", chinese},
		{"Windows other locale", nil, "de-DE", english},
		{"nothing set", nil, "", english},
		{"zhuang is not Chinese", map[string]string{"LANG": "zha_CN"}, "", english},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := detectLanguage(func(name string) string { return test.env[name] }, func() string { return test.system })
			if got != test.want {
				t.Fatalf("detectLanguage() = %v, want %v", got, test.want)
			}
		})
	}
}

func renderHelp(t *testing.T, args ...string) string {
	t.Helper()
	cmd := NewRootCommand("1.2.3")
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	return out.String()
}

func setLocale(t *testing.T, value string) {
	t.Helper()
	for _, name := range []string{"LANGUAGE", "LC_ALL", "LC_MESSAGES"} {
		t.Setenv(name, "")
	}
	t.Setenv("LANG", value)
	t.Cleanup(func() { cliLanguage = english })
}

func TestChineseHelp(t *testing.T) {
	setLocale(t, "zh_CN.UTF-8")
	help := renderHelp(t, "server", "start", "--help")
	for _, want := range []string{"启动本地服务", "用法:", "[参数]", "服务监听地址（默认 \"127.0.0.1:8585\"）", "显示 start 的帮助"} {
		if !strings.Contains(help, want) {
			t.Errorf("help is missing %q:\n%s", want, help)
		}
	}
	if version := renderHelp(t, "--version"); version != "steward version 1.2.3\n" {
		t.Errorf("version = %q, want unlocalized output", version)
	}
}

func TestEnglishHelpForOtherLocales(t *testing.T) {
	setLocale(t, "fr_FR.UTF-8")
	help := renderHelp(t, "--help")
	for _, want := range []string{"Usage:", "Update Steward using the method it was installed with", "help for steward"} {
		if !strings.Contains(help, want) {
			t.Errorf("help is missing %q:\n%s", want, help)
		}
	}
}
