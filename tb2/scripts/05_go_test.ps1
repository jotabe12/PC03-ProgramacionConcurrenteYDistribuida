# Pruebas automáticas de Go con el detector de carreras (-race), dentro de un contenedor
# golang:1.24 (no hace falta tener Go ni gcc instalados en Windows). Usa el CSV y el XLSX de data\.
. "$PSScriptRoot\_comun.ps1"
Write-Host "go vet + go test -race (tarda 4-8 minutos: usa el dataset real y levanta clústeres de prueba)..." -ForegroundColor Cyan
$out = Join-Path $Evid "05_go_test.txt"
docker run --rm -v "${Root}:/src" -v tb2-gomod:/go/pkg/mod -w /src -e GOFLAGS=-mod=mod -e CGO_ENABLED=1 golang:1.24 sh -c "go version && go vet ./... && echo 'go vet: sin observaciones' && go test -race -count=1 -timeout 20m -v ./... 2>&1" | Tee-Object -FilePath $out
Write-Host "`nResumen:" -ForegroundColor Cyan
Select-String -Path $out -Pattern "^(ok|FAIL|--- |PASS)|DATA RACE" | ForEach-Object { $_.Line }
