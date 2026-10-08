$projectDir = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
go run (Join-Path $projectDir "cmd/mycode") --tui @args
