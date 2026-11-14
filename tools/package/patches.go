package main

// These small adaptations keep the installation identity separate from the
// product's executable name. Hashes pin the reviewed upstream inputs.
// The only updater adaptation is the desktop-entry registration identity;
// archive, delta, signing, and trust code stay upstream.
var patches = []patch{
	{
		File: "updater.go", SHA256: "d1fd3ea79d84ddfea42f6b6171a9cf79188b0b40d70f356ee5851f0630b53084",
		Replacements: []replacement{{`update.RefreshDesktopEntry(target, filepath.Base(startExe))`, `update.RefreshDesktopEntry(target, strings.Replace(filepath.Base(startExe), "Yes Man", "yesman", 1))`}},
	},
	{
		File: "cmd/mygo/bundle.go", SHA256: "dd85f78c8deae212e9439f587427d7f2f863109e84cdede85f6bb0810a5ddd3c",
		Replacements: []replacement{
			{`func writeLocalizations(c *Config, resources string, langs []appLanguage) error {`, `func writeLocalizations(c *Config, resources string, langs []appLanguage) error {
	if len(langs) == 0 { langs = []appLanguage{{Tag: "en"}} }`},
			{`"CFBundleInfoDictionaryVersion":        "6.0",`, `"CFBundleInfoDictionaryVersion":        "6.0",
		"CFBundleDevelopmentRegion":            "en",
		"CFBundleLocalizations":                []any{"en"},`},
			{`"CFBundleName":                         c.Name,`, `"CFBundleName":                         c.slug(),`},
			{`"CFBundleDisplayName":                  c.Name,`, `"CFBundleDisplayName":                  c.slug(),`},
			{`names := map[string]any{}`, `// Finder honors a localized name when the base name matches the
		// bundle's on-disk name. Keep that stable and localize the product.
		names := map[string]any{"CFBundleName": c.Name, "CFBundleDisplayName": c.Name}`},
			{`app := filepath.Join(dir, name+".app")`, `app := filepath.Join(dir, c.slug()+".app")`},
			{`if err := os.WriteFile(filepath.Join(contents, "PkgInfo"), []byte("APPL????"), 0o644); err != nil {`, `// Preserve scripts which launch the previous bundle executable directly.
	if err := os.Symlink(name, filepath.Join(contents, "MacOS", c.slug())); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(contents, "PkgInfo"), []byte("APPL????"), 0o644); err != nil {`},
		},
	},
	{
		File: "cmd/mygo/installscript.go", SHA256: "8ac748c7eafef90a5bd40ef33ece9e66dd2d86c28bff95d74a6654338929bfe1",
		Replacements: []replacement{{`"Bundle":     c.executableName() + ".app",`, `"Bundle":     c.slug() + ".app",`}},
	},
	{
		File: "cmd/mygo/build.go", SHA256: "eeae6bf873395f5de91872fde6eddf5a3792fef8754f4b13ac6a80a30235ceb2",
		Replacements: []replacement{
			{`exe := filepath.Join(stage, c.slug())`, `exe := filepath.Join(stage, c.executableName())`},
			{`// The app as installed: the bundle, else everything next to the`, `// Older updaters and existing shortcuts relaunch the old basename.
	// This tiny compatibility entry transfers all work to the real main
	// image. It is included before the installed-file list and signatures.
	if goos == "windows" || goos == "linux" {
		launcher := *c
		launcher.Main = "./tools/launcher"
		legacy := c.slug()
		flags := []string{"-trimpath", "-ldflags", "-s -w"}
		if goos == "windows" {
			legacy += ".exe"
			flags[2] += " -H=windowsgui"
		}
		if err := goBuild(context.Background(), &launcher, nil, filepath.Join(stage, legacy),
			[]string{"GOOS=" + goos, "GOARCH=" + goarch, "CGO_ENABLED=0"}, flags...); err != nil {
			return nil, err
		}
	}

	// The app as installed: the bundle, else everything next to the`},
		},
	},
	{
		File: "internal/linux/app.go", SHA256: "37c5ff2dea47d3a86718f1787a3954f7f57350fdf17c86696b5365bd91025757",
		Replacements: []replacement{{`gSetPrgname(cs(filepath.Base(exe)))`, `name := filepath.Base(exe)
		// GLib's program id also names the WebKitGTK profile and window
		// class. Keep those stable; the kernel process uses the real image.
		if name == "Yes Man" { name = "yesman" }
		gSetPrgname(cs(name))`}},
	},
	{
		File: "internal/windows/app.go", SHA256: "b63e6e7940bd2ca54385835f216452766fb1cfc69fd7fe42e592c571e8074894",
		Replacements: []replacement{{`userData := filepath.Join(dir, sanitize(b.name), "WebView2")`, `profile := b.name
	// Preserve the existing WebView2 profile across the display-name change.
	if profile == "Yes Man" { profile = "yesman" }
	userData := filepath.Join(dir, sanitize(profile), "WebView2")`}},
	},
}
