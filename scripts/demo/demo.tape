# doc/assets/demo.gif. Recorded by scripts/demo/record.sh, which stages the
# made-up accounts (stage.py) and runs this with the sandbox's HOME and PATH;
# run that, not vhs directly, or the gif shows your own machine.

Output doc/assets/demo.gif

Set Shell bash
Set FontSize 15
Set Width 1380
Set Height 720
Set Padding 18
# Catppuccin Mocha on the background kizuna paints, so the padding matches.
Set Theme {"name":"kizuna","background":"#080808","foreground":"#cdd6f4","cursor":"#f5e0dc","selection":"#585b70","black":"#45475a","red":"#f38ba8","green":"#a6e3a1","yellow":"#f9e2af","blue":"#89b4fa","magenta":"#f5c2e7","cyan":"#94e2d5","white":"#bac2de","brightBlack":"#585b70","brightRed":"#f38ba8","brightGreen":"#a6e3a1","brightYellow":"#f9e2af","brightBlue":"#89b4fa","brightMagenta":"#f5c2e7","brightCyan":"#94e2d5","brightWhite":"#a6adc8"}
Set TypingSpeed 70ms
Set CursorBlink false

Hide
Type "export PS1='\[\e[2m\]~/projects/kizuna\[\e[0m\] $ ' && cd ~/projects/kizuna && clear"
Enter
Sleep 500ms
Show

Type "kizuna"
Sleep 500ms
Enter
# The cockpit: every account, sorted by the quota it has left, and the selected
# one open in place with its recent conversations.
Sleep 3.5s

Up
Sleep 1.5s
Down
Sleep 700ms
Down
Sleep 2s

# The full sheet, and back.
Tab
Sleep 3s
Tab
Sleep 1s

# Resume any recent conversation in the folder it ran in.
Up
Sleep 800ms
Type "R"
Sleep 3s
Down
Sleep 800ms
Down
Sleep 1.5s
Escape
Sleep 1s

# Hand a conversation to another account when this one runs dry.
Type "H"
Sleep 3.5s
Escape
Sleep 1s

# Every action, with what it would act on right now.
Space
Sleep 3s
Escape
Sleep 800ms

# What each profile has wired in: status line, OpenUsage, ranma, messaging.
Type "I"
Sleep 3s
Escape
Sleep 1s

Type "q"
Sleep 600ms
