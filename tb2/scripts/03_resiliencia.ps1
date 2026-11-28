# Tolerancia a fallos: se detiene un nodo, la API sigue respondiendo con los otros dos,
# y al volver el nodo el coordinador le reenvía su parte del entrenamiento automáticamente.
. "$PSScriptRoot\_comun.ps1"
$r = [ordered]@{ fecha = (Get-Date).ToString("yyyy-MM-ddTHH:mm:ss") }
Connect-Api "admin" | Out-Null

function Predecir([int]$mes) {
    # años futuros distintos para no leer la caché
    $p = Invoke-Api POST "/api/v1/predict" @{ district = "MIRAFLORES"; mes = $mes; anio = 2030 + (Get-Random -Maximum 3) }
    [ordered]@{ status = $p.Status; nodesUsed = $p.Json.nodesUsed; treesUsed = $p.Json.treesUsed; fromCache = $p.Json.fromCache; topClass = $p.Json.topClass; clientMs = $p.ClientMs }
}

$h = Invoke-Api GET "/api/v1/health" -SinToken
if (-not $h.Json.model.trained) { throw "Primero debe existir un modelo (01_levantar.ps1 lo entrena con AUTO_TRAIN)" }

Write-Host "1) Con los 3 nodos" -ForegroundColor Cyan
$r.con_3_nodos = Predecir 1
Write-Host ("   nodos {0}, árboles {1}" -f $r.con_3_nodos.nodesUsed, $r.con_3_nodos.treesUsed)

Write-Host "2) Deteniendo ml-node-2" -ForegroundColor Cyan
docker compose stop ml-node-2 | Out-Null
Wait-Api -Condicion { param($j) $j.nodesConnected -eq 2 } -Segundos 60 -Mensaje "desconexión de ml-node-2" | Out-Null
$r.con_2_nodos = Predecir 2
Write-Host ("   HTTP {0}: nodos {1}, árboles {2} (el servicio sigue disponible)" -f $r.con_2_nodos.status, $r.con_2_nodos.nodesUsed, $r.con_2_nodos.treesUsed) -ForegroundColor Green
$r.health_con_2_nodos = (Invoke-Api GET "/api/v1/health" -SinToken).Json

Write-Host "3) Reiniciando ml-node-2 (proceso nuevo, sin modelo en memoria)" -ForegroundColor Cyan
$t0 = Get-Date
docker compose start ml-node-2 | Out-Null
Wait-Api -Condicion { param($j) @($j.model.nodesReady).Count -eq 3 } -Segundos 180 -Mensaje "reentrenamiento automático de ml-node-2" | Out-Null
$r.segundos_hasta_recuperar = [math]::Round(((Get-Date) - $t0).TotalSeconds, 1)
$r.con_3_nodos_recuperado = Predecir 3
Write-Host ("   Recuperado en {0} s (incluye arranque del contenedor y carga del CSV): nodos {1}, árboles {2}" -f $r.segundos_hasta_recuperar, $r.con_3_nodos_recuperado.nodesUsed, $r.con_3_nodos_recuperado.treesUsed) -ForegroundColor Green

$logs = docker compose logs --no-color --since 5m coordinator ml-node-2 | Select-String -Pattern "ml-node-2|reentren|listo de nuevo|desconectado|registrado"
$r.logs = @($logs | ForEach-Object { $_.Line })
Save-Text ([string[]]$r.logs) "03_resiliencia_logs.txt"
Save-Json $r "03_resiliencia.json"
