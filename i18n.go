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

	// Settings › yesman: the crew, and what new yesmen run as
	"Choose the model new yesmen run as in Settings › yesman.": "请先在 设置 › yesman 中选择新 yesman 使用的模型。",
	"pi has no model %s":             "pi 没有模型 %s",
	"%s does not take the %s effort": "%s 不支持 %s 推理强度",
	"Artificial Analysis has no scores for %s at this effort: give your own": "Artificial Analysis 没有 %s 在这个推理强度下的评分：请填写你的估计",
	"%s is in the crew already":                              "%s 已在列表中",
	"%s is not in the crew":                                  "%s 不在列表中",
	"the intelligence score must be above 0 and at most 100": "智力分必须大于 0，且不超过 100",
	"scores cannot be below zero":                            "评分不能小于 0",
	"coding is a percentage, at most 100":                    "编码是百分比，不能超过 100",

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
	"Show Done Workers":   "显示已完成的助手",
	"Hide Done Workers":   "隐藏已完成的助手",
	"Open Conversation":   "打开对话",
	"View Workspace":      "查看工作区",
	"Reveal Workspace":    "显示工作区位置",
	"Reveal Knowledge":    "显示知识笔记位置",
	"Reveal Task Log":     "显示任务日志位置",
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
	"Open Task Log":           "打开任务日志",
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

	// Settings › Secrets and Variables: dialogs

	// Settings › Secrets and Variables: what cannot be kept
	"%s is a variable already: remove it from Variables first": "%s 已经是变量：请先从变量中移除",
	"%s is a secret already: remove it from Secrets first":     "%s 已经是机密：请先从机密中移除",
	"every entry needs a name":                                 "每一项都需要名称",
	"%s is longer than 128 characters":                         "%s 超过 128 个字符",
	"%q is not an environment variable name: use letters, digits and underscores, not starting with a digit": "%q 不是环境变量名：请使用字母、数字和下划线，且不要以数字开头",
	"%s is set by the system, so it cannot be set here":                                                      "%s 由系统设置，不能在这里设置",
	"%s is set by yesman or pi, so it cannot be set here":                                                    "%s 由 yesman 或 pi 设置，不能在这里设置",
	"%s ends in %s, which sops keeps unencrypted: name the secret otherwise":                                 "%s 以 %s 结尾，sops 会以明文保存它：请换一个名称",
	"%s is there twice": "%s 出现了两次",
	"the value of %s has a NUL character, which an environment variable cannot hold":     "%s 的值含有 NUL 字符，环境变量无法保存它",
	"the file should be a mapping with version and %s":                                   "文件应是包含 version 和 %s 的映射",
	"version should be a number":                                                         "version 应为数字",
	"this file is for a newer yesman (version %d); update yesman to read it":             "这个文件来自更新版本的 yesman（版本 %d）；请更新 yesman 后再读取",
	"version %d is unknown":                                                              "未知的版本 %d",
	"line %d: %s should be a mapping of names to values":                                 "第 %d 行：%s 应是从名称到值的映射",
	"line %d: %q is not a key of this file; it has version and %s":                       "第 %d 行：%q 不是这个文件的键；它只有 version 和 %s",
	"line %d: a name should be a word":                                                   "第 %d 行：名称应是一个单词",
	"line %d: %s has %q; an entry has only value and description":                        "第 %d 行：%s 含有 %q；每一项只有 value 和 description",
	"line %d: %s should have a value, or a value and a description":                      "第 %d 行：%s 应有一个值，或一个值和一段说明",
	"line %d: a value should be text":                                                    "第 %d 行：值应是文本",
	"this is not YAML: %s":                                                               "这不是 YAML：%s",
	"line %d: %s is there twice":                                                         "第 %d 行：%s 出现了两次",
	"the secrets are locked: unlock them with their password":                            "机密已锁定：请用密码解锁",
	"secrets are not set up yet":                                                         "还没有设置机密",
	"the password is wrong":                                                              "密码错误",
	"the password should have at least %d characters":                                    "密码至少需要 %d 个字符",
	"secrets are set up already":                                                         "机密已经设置好了",
	"%s is there without its key, identity.age: put the key back, or move the file away": "%s 存在，但缺少它的密钥 identity.age：请放回密钥，或把这个文件移走",
	"the secrets file is also encrypted to %s, which yesman cannot encrypt to":           "机密文件还加密给了 %s，yesman 无法为它加密",
	"the secrets file has secrets, so it stays":                                          "机密文件中还有机密，因此保留它",

	// Settings › Secrets: the password in the keychain
	"this system keeps no passwords for apps: choose a password for the secrets": "这个系统不能为应用保存密码：请为机密设置一个密码",
	"the keychain did not keep the password: %s":                                 "钥匙串没有保存密码：%s",
	"the keychain has no password for these secrets: enter it":                   "钥匙串中没有这些机密的密码：请输入",
	"the keychain has no password for these secrets":                             "钥匙串中没有这些机密的密码",

	// The configuration file's variables and secrets
	"secrets: the key is not an age file":                                                        "secrets：key 不是 age 文件",
	"the file's password does not open its secrets: %s":                                          "文件中的密码打不开它的机密：%s",
	"the file has no password for its secrets, so they cannot join the secrets here":             "文件中没有机密的密码，因此无法并入这里的机密",
	"the secrets here are locked: unlock them in Settings › Secrets, then import the file again": "这里的机密已锁定：请在 设置 › 机密 中解锁，再重新导入文件",
	"the variables and secrets would both set %s: rename one, then import again":                 "变量和机密会同时设置 %s：请重命名其中一个，再重新导入",
}
