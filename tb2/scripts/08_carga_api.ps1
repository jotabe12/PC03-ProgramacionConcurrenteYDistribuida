# Prueba de carga concurrente de POST /api/v1/predict con el generador en Go (cmd/loadtest),
# ejecutado como contenedor dentro de la red de Docker: 10, 50 y 100 clientes simultáneos,
# 600 consultas nuevas (van al clúster) y luego las mismas 600 (salen de Redis) por nivel.
param([string]$Niveles = "10,50,100")
. "$PSScriptRoot\_comun.ps1"
Write-Host "Generador de carga en Go (goroutines como clientes) contra el coordinador..." -ForegroundColor Cyan
$json = docker compose --profile herramientas run --rm -T loadtest -url http://coordinator:8080 -levels $Niveles
if ($LASTEXITCODE -ne 0) { throw "La prueba de carga falló" }
$texto = ($json | Out-String)
$rep = $texto | ConvertFrom-Json
$rep | Add-Member -NotePropertyName entorno -NotePropertyValue (Get-Entorno)
$rep.results | Select-Object scenario, clients, ok, errors, throughputRps, p50Ms, p95Ms, p99Ms, maxMs, pctUnder100ms | Format-Table -AutoSize
$peor = ($rep.results | Measure-Object -Property p99Ms -Maximum).Maximum
Write-Host ("Peor p99 en todos los niveles: {0} ms (límite del enunciado: 100 ms)" -f $peor) -ForegroundColor Green
Save-Json $rep "08_carga_api.json"
Save-Csv ($rep.results | Select-Object scenario, clients, requests, ok, errors, fromCache, durationS, throughputRps, avgMs, p50Ms, p95Ms, p99Ms, maxMs, serverP50Ms, serverP99Ms, pctUnder100ms) "08_carga_api.csv"
