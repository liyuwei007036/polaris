$ErrorActionPreference = 'Stop'

$projectRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
$goBinary = if ($env:GO_BINARY) { $env:GO_BINARY } else { Join-Path $projectRoot '.tools\go\go\bin\go.exe' }

Push-Location $projectRoot
try {
    & $goBinary test -tags=e2e -count=1 -v ./e2e
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }

    npm --prefix webui run test:e2e
    if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }
} finally {
    Pop-Location
}
