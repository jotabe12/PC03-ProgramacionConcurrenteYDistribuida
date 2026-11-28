# Evidencias de contenedores, bases de datos y aislamiento de red (mínimo privilegio, equivalente a una ACL).
. "$PSScriptRoot\_comun.ps1"
$envs = Read-DotEnv
$sec = @()   # resumen de pruebas de seguridad de red

Write-Host "1) Contenedores y redes" -ForegroundColor Cyan
docker compose ps | Save-Tee (Join-Path $Evid "04_docker_compose_ps.txt")
docker stats --no-stream --format "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.NetIO}}" | Save-Tee (Join-Path $Evid "04_docker_stats.txt")
$redes = @()
foreach ($n in @("frontend", "cluster", "datos")) {
    $info = docker network inspect "tb2-grupo3_$n" --format "{{.Name}} interna={{.Internal}} subred={{range .IPAM.Config}}{{.Subnet}}{{end}} contenedores={{range .Containers}}{{.Name}}({{.IPv4Address}}) {{end}}"
    $redes += $info
}
$redes | ForEach-Object { Write-Host "   $_" }
Save-Text ([string[]]$redes) "04_redes.txt"

Write-Host "2) MongoDB (base tb2_denuncias)" -ForegroundColor Cyan
$js = @'
db = db.getSiblingDB('tb2_denuncias');
print('Base de datos: tb2_denuncias');
db.getCollectionNames().sort().forEach(function (c) { print('  ' + c + ': ' + db.getCollection(c).countDocuments() + ' documentos'); });
print('');
print('--- usuario de la aplicacion (privilegio minimo y restriccion por IP) ---');
printjson(db.getUser('tb2_app', { showAuthenticationRestrictions: true }));
print('');
print('--- users (la contrasena se guarda como hash PBKDF2, nunca en claro; se muestran solo los primeros caracteres) ---');
printjson(db.users.aggregate([{ $project: { _id: 0, username: 1, role: 1, iterations: 1, updatedAt: 1, salt: { $concat: [{ $substrCP: ['$salt', 0, 8] }, '...'] }, hash: { $concat: [{ $substrCP: ['$hash', 0, 12] }, '...'] } } }]).toArray());
print('');
print('--- training_jobs (ultimos 3, resumidos) ---');
printjson(db.training_jobs.find({}, { _id: 0, jobId: 1, version: 1, requestedBy: 1, mode: 1, featureSet: 1, durationMs: 1, nodeCount: 1, totalTrees: 1, trainRecords: 1, 'evaluation.accuracy': 1, 'evaluation.top3Accuracy': 1, 'evaluation.macroF1': 1 }).sort({ createdAt: -1 }).limit(3).toArray());
print('');
print('--- predictions (ultimas 2) ---');
printjson(db.predictions.find({}, { _id: 0 }).sort({ createdAt: -1 }).limit(2).toArray());
print('');
print('--- impact_reports (ultimo, resumen y 3 filas) ---');
printjson(db.impact_reports.find({}, { _id: 0, anio: 1, mes: 1, orden: 1, modelVersion: 1, summary: 1, backtest: 1, rows: { $slice: 3 } }).sort({ createdAt: -1 }).limit(1).toArray());
print('');
print('--- cluster_metrics (ultima instantanea, resumida) ---');
printjson(db.cluster_metrics.find({}, { _id: 0, timestamp: 1, requests: 1, cacheHitPct: 1, 'latency.predict_cluster': 1, 'nodes.id': 1, 'nodes.stats.cpuPct': 1, 'nodes.stats.rssMB': 1 }).sort({ timestamp: -1 }).limit(1).toArray());
'@
$js | docker compose exec -T mongo sh -c "cat > /tmp/tb2_consultas.js"
docker compose exec -T mongo mongosh --quiet -u root -p $envs["MONGO_ROOT_PASSWORD"] --authenticationDatabase admin /tmp/tb2_consultas.js | Save-Tee (Join-Path $Evid "04_mongodb.txt")

Write-Host "3) Redis (usuario ACL tb2app)" -ForegroundColor Cyan
$rc = { param([string[]]$cmd) docker compose exec -T -e "REDISCLI_AUTH=$($envs['REDIS_PASSWORD'])" redis redis-cli --user tb2app @cmd 2>&1 }
$salida = @()
$salida += "> DBSIZE"; $salida += (& $rc @("DBSIZE"))
$salida += "> SCAN de claves tb2:* (primeras 25)"
$claves = @(& $rc @("--scan", "--pattern", "tb2:*") | Sort-Object)
$salida += ($claves | Select-Object -First 25)
$pred = $claves | Where-Object { $_ -like "tb2:pred:*" } | Select-Object -First 1
if ($pred) {
    $salida += "> TTL $pred   (segundos que le quedan en caché)"; $salida += (& $rc @("TTL", "$pred"))
    $salida += "> GET $pred"; $salida += (& $rc @("GET", "$pred"))
}
$salida | Save-Tee (Join-Path $Evid "04_redis.txt")

Write-Host "4) Aislamiento y mínimo privilegio (deben FALLAR los accesos no autorizados)" -ForegroundColor Cyan
function Add-Prueba($nombre, $esperado, $salidaCmd, [bool]$bloqueado) {
    $txt = (($salidaCmd | Out-String).Trim() -replace "\s+", " ")
    if ($txt.Length -gt 160) { $txt = $txt.Substring(0, 160) + "..." }
    $script:sec += [pscustomobject][ordered]@{ prueba = $nombre; esperado = $esperado; resultado = $(if ($bloqueado) { "BLOQUEADO (correcto)" } else { "PERMITIDO" }); detalle = $txt }
}
# a) Redis sin usuario (default deshabilitado)
$o = docker compose exec -T redis redis-cli PING 2>&1
Add-Prueba "Redis sin autenticación" "NOAUTH" $o (($o | Out-String) -match "NOAUTH|NOPERM|auth")
# b) Redis: tb2app intenta escribir fuera de tb2:*
$o = & $rc @("SET", "otra:clave", "1")
Add-Prueba "Redis: tb2app escribe la clave otra:clave" "NOPERM" $o (($o | Out-String) -match "NOPERM")
# c) Redis: tb2app intenta un comando peligroso
$o = & $rc @("FLUSHALL")
Add-Prueba "Redis: tb2app ejecuta FLUSHALL" "NOPERM" $o (($o | Out-String) -match "NOPERM")
# d) MongoDB: usuario de la app desde una IP distinta a la del coordinador
$o = docker compose exec -T mongo mongosh --quiet -u tb2_app -p $envs["MONGO_APP_PASSWORD"] --authenticationDatabase tb2_denuncias --eval "db.runCommand({ping:1})" 2>&1
Add-Prueba "MongoDB: tb2_app desde 127.0.0.1 (no es el coordinador)" "Authentication failed" $o (($o | Out-String) -match "Authentication failed|auth")
# e) Un nodo ML intenta llegar a MongoDB y Redis (red cluster, sin ruta a la red datos)
$o = docker compose exec -T ml-node-1 sh -c "nc -z -w 3 mongo 27017 && echo CONECTADO || echo SIN-ACCESO" 2>&1
Add-Prueba "ml-node-1 -> mongo:27017" "sin acceso" $o (-not (($o | Out-String) -match "CONECTADO"))
$o = docker compose exec -T ml-node-1 sh -c "nc -z -w 3 redis 6379 && echo CONECTADO || echo SIN-ACCESO" 2>&1
Add-Prueba "ml-node-1 -> redis:6379" "sin acceso" $o (-not (($o | Out-String) -match "CONECTADO"))
# f) nginx (red frontend) intenta llegar a MongoDB
$o = docker compose exec -T web sh -c "nc -z -w 3 mongo 27017 && echo CONECTADO || echo SIN-ACCESO" 2>&1
Add-Prueba "web (nginx) -> mongo:27017" "sin acceso" $o (-not (($o | Out-String) -match "CONECTADO"))
# g) Desde el equipo: solo el 8080 (UI) está publicado
foreach ($p in @(27017, 6379, 9090)) {
    $ok = $false
    try { $c = New-Object System.Net.Sockets.TcpClient; $ar = $c.BeginConnect("127.0.0.1", $p, $null, $null); $ok = $ar.AsyncWaitHandle.WaitOne(1500) -and $c.Connected; $c.Close() } catch { $ok = $false }
    Add-Prueba "Equipo -> 127.0.0.1:$p" "cerrado" $(if ($ok) { "abierto" } else { "conexión rechazada" }) (-not $ok)
}
$o = Invoke-Api GET "/api/v1/health" -SinToken
Add-Prueba "Equipo -> 127.0.0.1:8080 (UI y API por nginx)" "abierto (única puerta)" "HTTP $($o.Status)" ($o.Status -ne 200)
$sec | Format-Table prueba, esperado, resultado -AutoSize
Save-Json $sec "04_seguridad_red.json"

Write-Host "5) Logs (últimas líneas de cada servicio)" -ForegroundColor Cyan
$logs = docker compose logs --no-color --tail 40 coordinator ml-node-1 ml-node-2 ml-node-3 web
Save-Text ([string[]]$logs) "04_logs.txt"
