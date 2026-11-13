package main

import (
	"fmt"
	"slices"

	"github.com/egoist/mygo/i18n"
	_ "github.com/egoist/mygo/locales/zh-Hans" // MyGo's own strings: dialog buttons and menu roles
)

// yesman is written in English and translated to Simplified Chinese. It
// shows the language chosen in Settings › General, which MyGo keeps
// (App.SetLocale), or else the one of these its user reads best. The page
// translates itself (web/i18n.js); tr translates what Go shows: the tray,
// menus, notifications and dialogs.

// languages are the languages yesman is written in, its own first.
var languages = []string{"en", "zh-Hans"}

// languageSystem is the choice that follows the system's languages.
const languageSystem = "system"

// chosenLanguage returns a choice of the language menu for the tag MyGo
// keeps: languageSystem for none, or a language yesman is written in.
func chosenLanguage(tag string) string {
	if tag == "" {
		return languageSystem
	}
	if m := i18n.Match([]string{tag}, languages); m != "" {
		return m
	}
	return languageSystem
}

// languageTag returns the tag for App.SetLocale of a choice of the
// language menu, "" to follow the system, and whether yesman offers it.
func languageTag(choice string) (string, bool) {
	if choice == languageSystem {
		return "", true
	}
	return choice, slices.Contains(languages, choice)
}

// tr says msg, an English message, in the language yesman shows, with
// args filled in as fmt.Sprintf fills them.
func tr(msg string, args ...any) string { return trIn(i18n.Locale(), msg, args...) }

// trIn says msg in locale.
func trIn(locale, msg string, args ...any) string {
	if locale == "zh-Hans" {
		if s, ok := zhHans[msg]; ok {
			msg = s
		}
	}
	if len(args) == 0 {
		return msg
	}
	return fmt.Sprintf(msg, args...)
}

// inUseText translates what pool.InUse says of a repository work depends
// on.
func inUseText(reason string) string {
	var n int
	if _, err := fmt.Sscanf(reason, "%d worktrees are checked out of it", &n); err == nil {
		return tr("%d worktrees are checked out of it", n)
	}
	return tr(reason)
}

// zhHans translates the English messages Go shows to Simplified Chinese.
// Each keeps the verbs of its English message, in their order.
var zhHans = map[string]string{
	// Settings › pi: sources refused before pi sees them
	"enter a package source, such as npm:pi-web-access": "请输入包来源，例如 npm:pi-web-access",
	"%q is not a package source":                        "%q 不是包来源",
	"a package source is one line":                      "包来源只能有一行",
	"%q names no version after @":                       "%q 的 @ 后面没有版本",
	"%q is not an npm package name":                     "%q 不是 npm 包名",
	"%q is not a version":                               "%q 不是版本号",
	"%q is not a git source":                            "%q 不是 git 来源",
	"%q is not a package source: use npm:name, git:host/path, a URL or a local path": "%q 不是包来源：请使用 npm:名称、git:主机/路径、URL 或本地路径",
	"pi %s took too long": "pi %s 超时",

	// The tray
	"Open yesman":                            "打开 yesman",
	"Quit yesman":                            "退出 yesman",
	"yesman: %d working, %d waiting for you": "yesman：%d 个在工作，%d 个在等你",

	// Notifications
	"%s needs you": "%s 需要你",
	"It asked a question and is waiting for your answer.": "它提了一个问题，正在等你回答。",
	"%s stopped":                    "%s 已停止",
	"Finished and waiting for you.": "已完成，等你继续。",

	// The agent menu
	"Open Conversation":   "打开对话",
	"View Workspace":      "查看工作区",
	"Reveal Workspace":    "显示工作区位置",
	"Reveal Knowledge":    "显示知识笔记位置",
	"Reveal Session Logs": "显示会话日志位置",
	"Stop Current Run":    "停止当前运行",
	"Put to Sleep":        "让它休眠",
	"Remove Worker…":      "移除助手…",
	"Archive yesman…":     "归档 yesman…",

	// The files pane's menus
	"Go to File…":             "转到文件…",
	"Show File Tree":          "显示文件树",
	"Collapse All Folders":    "折叠所有文件夹",
	"Auto Save":               "自动保存",
	"Word Wrap":               "自动换行",
	"Open Knowledge Notes":    "打开知识笔记",
	"Check Out a Repository…": "检出仓库…",
	"Dismiss yesman…":         "解雇 yesman…",
	"Reveal in Finder":        "在访达中显示",
	"Show in Explorer":        "在资源管理器中显示",
	"Show in Files":           "在文件管理器中显示",
	"Open":                    "打开",
	"Copy Path":               "拷贝路径",
	"Copy Relative Path":      "拷贝相对路径",

	// Dialogs
	"Choose a yesman folder":      "选择 yesman 文件夹",
	"Choose folder":               "选择文件夹",
	"Export yesman configuration": "导出 yesman 配置",
	"Import yesman configuration": "导入 yesman 配置",
	"yesman configuration":        "yesman 配置",
	"Export":                      "导出",
	"Choose":                      "选择",

	// Storage
	"enter the full path of the yesman folder": "请输入 yesman 文件夹的完整路径",
	"choose a folder, not a file":              "请选择文件夹，而不是文件",
	"YESMAN_HOME sets the folder for this launch; unset it and restart to change the folder in Settings": "本次启动的文件夹由 YESMAN_HOME 指定；取消设置并重启后，才能在设置中更改",

	// Why importing a configuration keeps a repository
	"a worktree is checked out of it":                       "有工作树从它检出",
	"%d worktrees are checked out of it":                    "有 %d 个工作树从它检出",
	"a branch has commits that no remote has":               "有分支包含远程仓库没有的提交",
	"an agent is cloning it":                                "有智能体正在克隆它",
	"yesman could not check whether work depends on it: %s": "yesman 无法确认是否有工作依赖它：%s",
	"removing it failed: %s":                                "移除失败：%s",
}
