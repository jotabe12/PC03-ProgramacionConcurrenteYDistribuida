# Construye las imágenes y levanta los 7 contenedores del TB2:
# web (nginx + SPA), coordinador, 3 nodos ML, MongoDB y Redis, en 3 redes.
# Uso:  .\scripts\01_levantar.ps1            (conserva los datos de MongoDB)
#       .\scripts\01_levantar.ps1 -Limpio    (borra el volumen de MongoDB para empezar de cero)
param([switch]$Limpio)
. "$PSScriptRoot\_comun.ps1"

# 1) Datos: el CSV vive en la raíz del repositorio; el XLSX del INEI ya está en data\.
$csv = Join-Path $Root "data\DATASET_Denuncias Policiales - Enero 2018 a Septiembre 2026.csv"
if (-not (Test-Path $csv)) {
    $original = Join-Path (Split-Path $Root -Parent) "DATASET_Denuncias Policiales - Enero 2018 a Septiembre 2026.csv"
    if (-not (Test-Path $original)) { throw "No se encuentra el CSV en data\ ni en la carpeta del repositorio" }
    Write-Host "Copiando el CSV oficial a data\ ..." -ForegroundColor Cyan
    Copy-Item $original $csv
}
$xlsx = Get-ChildItem (Join-Path $Root "data") -Filter "*poblacion*.xlsx" | Select-Object -First 1
if (-not $xlsx) { throw "Falta el XLSX de población del INEI en data\" }

# 2) .env con secretos aleatorios (solo la primera vez).
$envPath = Join-Path $Root ".env"
if (-not (Test-Path $envPath)) {
    Write-Host "Creando .env con claves aleatorias..." -ForegroundColor Cyan
    $rng = [System.Security.Cryptography.RandomNumberGenerator]::Create()
    $chars = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789".ToCharArray()
    function New-Secreto([int]$n) {
        $b = New-Object byte[] $n
        $rng.GetBytes($b)
        -join ($b | ForEach-Object { $chars[$_ % $chars.Length] })
    }
    $lineas = foreach ($l in [System.IO.File]::ReadAllLines((Join-Path $Root ".env.example"))) {
        if ($l -match '^(JWT_SECRET)=generar$') { "$($Matches[1])=$(New-Secreto 48)" }
        elseif ($l -match '^([A-Z_]+)=generar$') { "$($Matches[1])=$(New-Secreto 24)" }
        else { $l }
    }
    [System.IO.File]::WriteAllLines($envPath, [string[]]$lineas, $Utf8)
}
$envs = Read-DotEnv

# 3) La PC4 usa los mismos puertos: se detiene si está levantada.
$pc4 = docker compose -p pc4-grupo3 ps -q 2>$null
if ($pc4) {
    Write-Host "Deteniendo el stack de la PC4 (usa el puerto 8080)..." -ForegroundColor Cyan
    docker compose -p pc4-grupo3 down
}

if ($Limpio) {
    Write-Host "0) Eliminando contenedores y volumen de MongoDB anteriores..." -ForegroundColor Cyan
    docker compose down -v
}
Write-Host "1) Construyendo e iniciando los contenedores (la primera vez descarga imágenes, módulos de Go y paquetes de npm)..." -ForegroundColor Cyan
$inicio = Get-Date
docker compose up -d --build
if ($LASTEXITCODE -ne 0) { throw "docker compose up falló (¿Docker Desktop está abierto? ¿puerto 8080 libre?)" }

Write-Host "2) Esperando a los 3 nodos ML y al primer entrenamiento automático (AUTO_TRAIN)..." -ForegroundColor Cyan
$h = Wait-Api -Condicion { param($j) $j.nodesConnected -ge 3 -and $j.model.trained } -Segundos 300 -Mensaje "nodos y modelo"
$seg = [math]::Round(((Get-Date) - $inicio).TotalSeconds, 1)
Write-Host ("   Listo en {0} s: {1} nodos ({2}); modelo v{3} ({4}); MongoDB {5}; Redis {6}" -f $seg, $h.Json.nodesConnected, ($h.Json.nodes -join ", "),
    $h.Json.model.version, $h.Json.model.featureSet, $h.Json.storage.mongo, $h.Json.storage.redis) -ForegroundColor Green

docker compose ps
$ps = docker compose ps --format "table {{.Name}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}"
Save-Text ([string[]]$ps) "01_docker_compose_ps.txt"
Write-Host ""
Write-Host "Aplicación web:  http://localhost:8080" -ForegroundColor Yellow
Write-Host "Swagger UI:      http://localhost:8080/swagger" -ForegroundColor Yellow
Write-Host ("Usuarios:        {0} / {1}   (admin)" -f $envs["ADMIN_USER"], $envs["ADMIN_PASSWORD"]) -ForegroundColor Yellow
Write-Host ("                 {0} / {1}   (analista)" -f $envs["ANALISTA_USER"], $envs["ANALISTA_PASSWORD"]) -ForegroundColor Yellow
