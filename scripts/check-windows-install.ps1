param([string]$Package, [string]$Web, [string]$Evidence)
$ErrorActionPreference = 'Stop'
if ($env:GITHUB_ACTIONS -ne 'true' -or $env:RUNNER_ENVIRONMENT -ne 'github-hosted') { throw 'Disposable hosted Windows runner required' }
$Package = (Resolve-Path -LiteralPath $Package).Path
$Web = (Resolve-Path -LiteralPath $Web).Path
New-Item -ItemType Directory -Force -Path $Evidence | Out-Null
$Evidence = (Resolve-Path -LiteralPath $Evidence).Path
$key = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\dev.idy.yesman'
if (Test-Path -LiteralPath $key) { throw 'Existing installation found; refusing to run' }
$root = Join-Path $env:RUNNER_TEMP ('Yes Man install fixture ' + [guid]::NewGuid().ToString('N'))
$install = Join-Path $root 'Original yesman install'
New-Item -ItemType Directory -Path $install -Force | Out-Null
$sentinel = Join-Path $root 'workspace-vault-session-sentinel'
[IO.File]::WriteAllText($sentinel, 'preserved')
[IO.File]::WriteAllText((Join-Path $install 'yesman.exe'), 'previous executable')
New-Item -Path $key -Force | Out-Null
Set-ItemProperty -Path $key -Name InstallLocation -Value $install
Set-ItemProperty -Path $key -Name DisplayName -Value 'yesman'
$shell = New-Object -ComObject WScript.Shell
$programs = $shell.SpecialFolders.Item('Programs')
$desktop = $shell.SpecialFolders.Item('Desktop')
foreach ($folder in @($programs, $desktop)) {
    $old = $shell.CreateShortcut((Join-Path $folder 'yesman.lnk'))
    $old.TargetPath = Join-Path $install 'yesman.exe'
    $old.Save()
}
try {
    $setup = @(Get-ChildItem -LiteralPath $Package -Filter 'yesman-setup-*.exe')
    if ($setup.Count -ne 1) { throw 'Expected exactly one installer' }
    # No /D override: the stable uninstall registry key must find the old
    # installation, including its spaces, rather than creating a second app.
    $p = Start-Process -FilePath $setup[0].FullName -ArgumentList '/S' -Wait -PassThru
    if ($p.ExitCode -ne 0) { throw "Installer failed: $($p.ExitCode)" }
    $location = (Get-ItemProperty -Path $key).InstallLocation
    if ($location.TrimEnd('\') -ne $install) { throw "Installation moved: $location" }
    if ((Get-ItemProperty -Path $key).DisplayName -ne 'Yes Man') { throw 'Wrong installed app display name' }
    $shortcuts = @()
    foreach ($folder in @($programs, $desktop)) {
        if (Test-Path -LiteralPath (Join-Path $folder 'yesman.lnk')) { throw 'Old standard shortcut duplicated' }
        $path = Join-Path $folder 'Yes Man.lnk'
        $link = $shell.CreateShortcut($path)
        if ($link.TargetPath -ne (Join-Path $install 'Yes Man.exe')) { throw "Wrong shortcut target: $($link.TargetPath)" }
        $shortcuts += @{ path=$path; target=$link.TargetPath; arguments=$link.Arguments }
    }
    @{location=$location;displayName='Yes Man';shortcuts=$shortcuts} | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath (Join-Path $Evidence 'installer.json')
    python scripts/check-process-name.py $install $Web $Evidence
    if ($LASTEXITCODE -ne 0) { throw 'Native process smoke failed' }
    # /S and _?= keep the uninstaller in this synthetic install, so waiting
    # tracks completion rather than its temporary-copy launcher.
    $uninstall = Join-Path $install 'Uninstall.exe'
    $p = Start-Process -FilePath $uninstall -ArgumentList "/S _?=$install" -Wait -PassThru
    if ($p.ExitCode -ne 0 -or (Test-Path -LiteralPath $key)) { throw 'Uninstall failed' }
    foreach ($folder in @($programs, $desktop)) {
        if (Test-Path -LiteralPath (Join-Path $folder 'Yes Man.lnk')) { throw 'Uninstall left a standard shortcut' }
    }
    if ([IO.File]::ReadAllText($sentinel) -ne 'preserved') { throw 'User data sentinel changed' }
    @{passed=$true;sentinel='preserved';registryRemoved=$true;shortcutsRemoved=$true} | ConvertTo-Json | Set-Content -LiteralPath (Join-Path $Evidence 'uninstall.json')
} finally {
    # This runner is disposable. Failures preserve logs/artifacts; never run
    # the fixture or its cleanup against a workstation installation.
    [Runtime.InteropServices.Marshal]::FinalReleaseComObject($shell) | Out-Null
}
