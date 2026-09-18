package cli

import (
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
)

type language int

const (
	english language = iota
	chinese
)

// cliLanguage is chosen once when the root command is built.
var cliLanguage = english

// tr returns the text for the terminal's language.
func tr(en string, zh string) string {
	if cliLanguage == chinese {
		return zh
	}
	return en
}

// detectLanguage follows gettext precedence: LANGUAGE, LC_ALL, LC_MESSAGES,
// then LANG. Without any of them it asks the operating system. Chinese
// locales select Chinese; every other locale uses English.
func detectLanguage(getenv func(string) string, system func() string) language {
	for _, name := range []string{"LANGUAGE", "LC_ALL", "LC_MESSAGES", "LANG"} {
		value := strings.TrimSpace(getenv(name))
		if value == "" {
			continue
		}
		if name == "LANGUAGE" {
			// A priority list such as "zh_CN:en"; only the first entry decides.
			value, _, _ = strings.Cut(value, ":")
		}
		return languageOf(value)
	}
	return languageOf(system())
}

func languageOf(locale string) language {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if locale == "zh" || strings.HasPrefix(locale, "zh_") || strings.HasPrefix(locale, "zh-") || strings.HasPrefix(locale, "zh.") {
		return chinese
	}
	return english
}

func setLanguageFromEnvironment() {
	cliLanguage = detectLanguage(os.Getenv, systemLocale)
}

// localizeCobra translates the help text and flags that cobra generates.
func localizeCobra(root *cobra.Command) {
	if cliLanguage == english {
		return
	}
	cobra.AddTemplateFunc("chineseUsage", chineseUsage)
	root.SetUsageTemplate(chineseUsageTemplate)
	// The version line stays unlocalized because scripts parse it.
	root.InitDefaultVersionFlag()
	if flag := root.Flags().Lookup("version"); flag != nil {
		flag.Usage = "显示版本"
	}
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	for _, command := range root.Commands() {
		switch command.Name() {
		case "help":
			command.Short = "显示任意命令的帮助"
		case "completion":
			command.Short = "生成指定 shell 的自动补全脚本"
			command.Long = "生成 " + root.Name() + " 的自动补全脚本。各子命令的帮助说明了脚本的使用方法。"
			for _, shell := range command.Commands() {
				shell.Short = "生成 " + shell.Name() + " 的自动补全脚本"
			}
		}
	}
	var walk func(*cobra.Command)
	walk = func(command *cobra.Command) {
		command.InitDefaultHelpFlag()
		if flag := command.Flags().Lookup("help"); flag != nil {
			flag.Usage = "显示 " + command.Name() + " 的帮助"
		}
		for _, child := range command.Commands() {
			walk(child)
		}
	}
	walk(root)
}

var flagDefault = regexp.MustCompile(`(?m) \(default (.*)\)$`)

// chineseUsage translates the fixed English fragments that cobra and pflag
// add to usage lines.
func chineseUsage(text string) string {
	text = strings.ReplaceAll(text, "[flags]", "[参数]")
	return flagDefault.ReplaceAllString(text, "（默认 $1）")
}

const chineseUsageTemplate = `用法:{{if .Runnable}}
  {{chineseUsage .UseLine}}{{end}}{{if .HasAvailableSubCommands}}
  {{.CommandPath}} [命令]{{end}}{{if gt (len .Aliases) 0}}

别名:
  {{.NameAndAliases}}{{end}}{{if .HasExample}}

示例:
{{.Example}}{{end}}{{if .HasAvailableSubCommands}}

可用命令:{{range .Commands}}{{if (or .IsAvailableCommand (eq .Name "help"))}}
  {{rpad .Name .NamePadding }} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableLocalFlags}}

参数:
{{.LocalFlags.FlagUsages | chineseUsage | trimTrailingWhitespaces}}{{end}}{{if .HasAvailableInheritedFlags}}

全局参数:
{{.InheritedFlags.FlagUsages | chineseUsage | trimTrailingWhitespaces}}{{end}}{{if .HasHelpSubCommands}}

其他帮助主题:{{range .Commands}}{{if .IsAdditionalHelpTopicCommand}}
  {{rpad .CommandPath .CommandPathPadding}} {{.Short}}{{end}}{{end}}{{end}}{{if .HasAvailableSubCommands}}

运行 "{{.CommandPath}} [命令] --help" 查看命令详情。{{end}}
`
