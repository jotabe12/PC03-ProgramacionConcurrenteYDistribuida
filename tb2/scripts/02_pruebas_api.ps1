# Pruebas funcionales y de latencia de la API del TB2 (requisito: < 100 ms), con autenticación JWT.
. "$PSScriptRoot\_comun.ps1"

$r = [ordered]@{}
$r.fecha = (Get-Date).ToString("yyyy-MM-ddTHH:mm:ss")
$r.entorno = Get-Entorno
Write-Host ("Entorno: {0} | {1} núcleos / {2} hilos | {3} GB RAM | {4}" -f $r.entorno.cpu, $r.entorno.nucleosFisicos, $r.entorno.hilosLogicos, $r.entorno.ramGB, $r.entorno.docker) -ForegroundColor Cyan

Write-Host "1) Estado (público) e inicio de sesión de los dos roles" -ForegroundColor Cyan
$h = Invoke-Api GET "/api/v1/health" -SinToken
$r.health_inicial = [ordered]@{ status = $h.Status; body = $h.Json }
$admin = Connect-Api "admin"
$ana = Connect-Api "analista"
$me = Invoke-Api GET "/api/v1/auth/me" -Token $ana
$r.login = [ordered]@{ admin = "token emitido ($($admin.Length) caracteres)"; analista_me = $me.Json }
Write-Host ("   Nodos {0}/{1}, modelo v{2} ({3}); analista: rol {4}, vence {5}" -f $h.Json.nodesConnected, $h.Json.nodesExpected, $h.Json.model.version, $h.Json.model.featureSet, $me.Json.role, $me.Json.expiresAt)

# 20 consultas distintas (10 distritos x 2 meses de 2026): la primera vez van al clúster.
$distritos = @("SAN JUAN DE LURIGANCHO", "LIMA", "CALLAO", "COMAS", "SAN MARTIN DE PORRES", "ATE", "LOS OLIVOS", "VILLA MARIA DEL TRIUNFO", "SANTIAGO DE SURCO", "VILLA EL SALVADOR")
$sufijo = Get-Random -Minimum 2027 -Maximum 2033   # año futuro aleatorio: asegura que no estén en caché
$consultas = @()
for ($i = 0; $i -lt 20; $i++) { $consultas += @{ district = $distritos[$i % 10]; mes = 1 + ($i % 12); anio = $sufijo } }

function Invoke-Serie {
    $filas = @()
    foreach ($c in $consultas) {
        $p = Invoke-Api POST "/api/v1/predict" $c -Token $ana
        $filas += [pscustomobject][ordered]@{
            district = $c.district; mes = $c.mes; anio = $c.anio; status = $p.Status; fromCache = $p.Json.fromCache
            nodesUsed = $p.Json.nodesUsed; treesUsed = $p.Json.treesUsed; topClass = $p.Json.topClass
            topProbability = [math]::Round([double]$p.Json.topProbability, 4); topStreetCrime = $p.Json.topStreetCrime
            clientMs = $p.ClientMs; serverMs = $p.ServerMs; clusterMs = $p.Json.clusterMs
        }
    }
    return ,$filas
}

Write-Host "2) 20 predicciones SIN caché (consultan a los 3 nodos por TCP), rol analista" -ForegroundColor Cyan
$frias = Invoke-Serie
Write-Host "3) Las mismas 20 predicciones CON caché (Redis)" -ForegroundColor Cyan
$calidas = Invoke-Serie
$r.predicciones_sin_cache = $frias
$r.predicciones_con_cache = $calidas
$r.resumen_latencia = [ordered]@{
    sin_cache_cliente  = Get-Resumen ($frias | ForEach-Object { $_.clientMs })
    sin_cache_servidor = Get-Resumen ($frias | ForEach-Object { $_.serverMs })
    con_cache_cliente  = Get-Resumen ($calidas | ForEach-Object { $_.clientMs })
    con_cache_servidor = Get-Resumen ($calidas | ForEach-Object { $_.serverMs })
    nota               = "cliente = Stopwatch en PowerShell (incluye nginx y HTTP local); servidor = cabecera X-Response-Time-Ms del coordinador"
}
$frias | Select-Object district, mes, fromCache, nodesUsed, treesUsed, topClass, topProbability, clientMs, serverMs | Format-Table -AutoSize
$s = $r.resumen_latencia
Write-Host ("   Sin caché: promedio {0} ms, p95 {1} ms, máximo {2} ms (cliente)" -f $s.sin_cache_cliente.promedioMs, $s.sin_cache_cliente.p95Ms, $s.sin_cache_cliente.maximoMs) -ForegroundColor Green
Write-Host ("   Con caché: promedio {0} ms, p95 {1} ms, máximo {2} ms (cliente)" -f $s.con_cache_cliente.promedioMs, $s.con_cache_cliente.p95Ms, $s.con_cache_cliente.maximoMs) -ForegroundColor Green

Write-Host "4) Ejemplo completo de predicción" -ForegroundColor Cyan
$ej = Invoke-Api POST "/api/v1/predict" @{ district = "San Juan de Lurigancho"; mes = 10; anio = 2026 }
$r.ejemplo_prediccion = $ej.Json
$ej.Json.probabilities | Format-Table -AutoSize

Write-Host "5) Validaciones y políticas de seguridad (códigos de error)" -ForegroundColor Cyan
$casos = @(
    @{ nombre = "mes fuera de rango"; metodo = "POST"; ruta = "/api/v1/predict"; body = @{ district = "MIRAFLORES"; mes = 13; anio = 2026 }; token = $ana; esperado = 400 },
    @{ nombre = "falta un campo"; metodo = "POST"; ruta = "/api/v1/predict"; body = @{ district = "MIRAFLORES" }; token = $ana; esperado = 400 },
    @{ nombre = "distrito inexistente"; metodo = "POST"; ruta = "/api/v1/predict"; body = @{ district = "NARNIA"; mes = 10; anio = 2026 }; token = $ana; esperado = 404 },
    @{ nombre = "método no permitido"; metodo = "GET"; ruta = "/api/v1/predict"; body = $null; token = $ana; esperado = 405 },
    @{ nombre = "impacto sin parámetros"; metodo = "GET"; ruta = "/api/v1/impacto"; body = $null; token = $ana; esperado = 400 },
    @{ nombre = "sin token"; metodo = "GET"; ruta = "/api/v1/districts"; body = $null; token = "SIN"; esperado = 401 },
    @{ nombre = "token alterado"; metodo = "GET"; ruta = "/api/v1/districts"; body = $null; token = ($ana + "x"); esperado = 401 },
    @{ nombre = "analista intenta entrenar"; metodo = "POST"; ruta = "/api/v1/train"; body = @{}; token = $ana; esperado = 403 },
    @{ nombre = "analista pide métricas"; metodo = "GET"; ruta = "/api/v1/metrics"; body = $null; token = $ana; esperado = 403 }
)
$validaciones = @()
foreach ($c in $casos) {
    if ($c.token -eq "SIN") { $v = Invoke-Api $c.metodo $c.ruta $c.body -SinToken } else { $v = Invoke-Api $c.metodo $c.ruta $c.body -Token $c.token }
    $validaciones += [pscustomobject][ordered]@{ caso = $c.nombre; esperado = $c.esperado; obtenido = $v.Status; correcto = ($v.Status -eq $c.esperado); codigo = $v.Json.code; clientMs = $v.ClientMs }
}
$r.validaciones = $validaciones
$validaciones | Format-Table -AutoSize

Write-Host "6) Pregunta de impacto social y plan de patrullaje" -ForegroundColor Cyan
$i1 = Invoke-Api GET "/api/v1/impacto?mes=3&anio=2026&top=10" -Token $ana
$i2 = Invoke-Api GET "/api/v1/impacto?mes=3&anio=2026&top=10" -Token $ana
$pp = Invoke-Api GET "/api/v1/patrullaje?mes=10&anio=2026&top=8" -Token $ana
$r.impacto = [ordered]@{ sin_cache = [ordered]@{ status = $i1.Status; clientMs = $i1.ClientMs; serverMs = $i1.ServerMs; summary = $i1.Json.summary; backtest = $i1.Json.backtest }
                         con_cache = [ordered]@{ status = $i2.Status; clientMs = $i2.ClientMs; fromCache = $i2.Json.fromCache } }
$r.patrullaje = $pp.Json
Write-Host ("   Impacto (50 distritos en un lote): {0} ms sin caché, {1} ms con caché" -f $i1.ClientMs, $i2.ClientMs) -ForegroundColor Green
Write-Host ("   Marzo 2026: el top-10 concentra {0:N1} % de los delitos de calle esperados y cubre {1:N0} habitantes; real {2:N1} % (óptimo {3:N1} %)" -f `
    $i1.Json.summary.expectedCoveragePct, $i1.Json.summary.populationTop, $i1.Json.backtest.realCoveragePct, $i1.Json.backtest.oracleCoveragePct) -ForegroundColor Green
$pp.Json.rows | Select-Object rank, district, zone, @{n = "esperados"; e = { [math]::Round($_.expectedStreet) } }, @{n = "tasa100k"; e = { [math]::Round($_.ratePer100k, 1) } }, topStreetCrime, franjaSugerida | Format-Table -AutoSize

Write-Host "7) Evaluación, métricas y estado final" -ForegroundColor Cyan
$ev = Invoke-Api GET "/api/v1/evaluacion" -Token $ana
$mt = Invoke-Api GET "/api/v1/metrics"
$hf = Invoke-Api GET "/api/v1/health" -SinToken
$r.evaluacion = $ev.Json
$r.metrics_final = $mt.Json
$r.health_final = $hf.Json
Write-Host ("   Modelo v{0}: exactitud {1:P2}, top-3 {2:P2}" -f $ev.Json.model.version, $ev.Json.current.accuracy, $ev.Json.current.top3Accuracy)
Write-Host ("   MongoDB: {0} | Redis: {1} claves" -f (($hf.Json.storage.collections.PSObject.Properties | ForEach-Object { "$($_.Name)=$($_.Value)" }) -join ", "), $hf.Json.storage.cacheKeys)

Save-Json $r "02_pruebas_api.json"
$malos = @($validaciones | Where-Object { -not $_.correcto })
if ($malos.Count -gt 0) { Write-Host "ATENCIÓN: $($malos.Count) validaciones no dieron el código esperado" -ForegroundColor Red }
Write-Host "`nListo. Siguiente: 03_resiliencia.ps1" -ForegroundColor Yellow
