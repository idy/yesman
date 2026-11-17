; The main image changed; older updates and shortcuts still use yesman.exe.
; The compatibility launcher keeps that target valid. On an installer run,
; replace the old standard shortcut, preserving the user's desktop choice.
!macro customInstall
  Delete "$SMPROGRAMS\yesman.lnk"
  IfFileExists "$DESKTOP\yesman.lnk" 0 +3
  Delete "$DESKTOP\yesman.lnk"
  Call CreateDesktopShortcut
!macroend

!macro customUnInstall
  Delete "$SMPROGRAMS\yesman.lnk"
  Delete "$DESKTOP\yesman.lnk"
!macroend
