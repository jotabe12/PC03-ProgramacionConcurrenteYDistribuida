# Prueba de punta a punta (como lo usaría el frontend) y de las políticas de seguridad de la API:
# login -> WebSocket -> entrenamiento con progreso en vivo -> predicción (evento en vivo) ->
# persistencia en MongoDB y caché en Redis; luego 401, 403, CORS, intentos de login (429) y cuota.
. "$PSScriptRoot\_comun.ps1"
$envs = Read-DotEnv
$r = [ordered]@{ fecha = (Get-Date).ToString("yyyy-MM-ddTHH:mm:ss") }
$pasos = @()
function Paso($n, $ok, $detalle) {
    $script:pasos += [pscustomobject][ordered]@{ paso = $n; correcto = [bool]$ok; detalle = "$detalle" }
    $color = "Green"; if (-not $ok) { $color = "Red" }
    Write-Host ("  [{0}] {1}: {2}" -f $(if ($ok) { "OK" } else { "FALLA" }), $n, $detalle) -ForegroundColor $color
}

Write-Host "1) Login de los dos roles" -ForegroundColor Cyan
$admin = Connect-Api "admin"
$ana = Connect-Api "analista"
Paso "login admin y analista" ($admin -and $ana) "tokens JWT HS256 emitidos"

Write-Host "2) WebSocket (token como subprotocolo) y eventos en vivo" -ForegroundColor Cyan
$ws = New-Object System.Net.WebSockets.ClientWebSocket
$ws.Options.AddSubProtocol("jwt")
$ws.Options.AddSubProtocol($admin)
$ws.ConnectAsync([Uri]"ws://localhost:8080/ws", [Threading.CancellationToken]::None).Wait(10000) | Out-Null
Paso "conexión WebSocket" ($ws.State -eq "Open") "estado $($ws.State), subprotocolo '$($ws.SubProtocol)'"
$buf = New-Object byte[] 262144
$seg = New-Object System.ArraySegment[byte] -ArgumentList @(, $buf)
$eventos = New-Object System.Collections.ArrayList
$script:pend = $null
$script:acc = ""
# Recibe eventos durante "ms" milisegundos. La lectura pendiente NO se cancela (cancelarla
# aborta el WebSocket en .NET): se espera con Wait() y se retoma en la siguiente llamada.
$recibir = {
    param([int]$ms)
    $fin = (Get-Date).AddMilliseconds($ms)
    while ($ws.State -eq "Open") {
        if ($null -eq $script:pend) { $script:pend = $ws.ReceiveAsync($seg, [Threading.CancellationToken]::None) }
        $resta = [int]($fin - (Get-Date)).TotalMilliseconds
        if ($resta -le 0) { break }
        if (-not $script:pend.Wait($resta)) { break }
        $res = $script:pend.Result
        $script:pend = $null
        $script:acc += [System.Text.Encoding]::UTF8.GetString($buf, 0, $res.Count)
        if ($res.EndOfMessage) {
            try { [void]$eventos.Add(($script:acc | ConvertFrom-Json)) } catch { }
            $script:acc = ""
        }
    }
}
& $recibir 1500
$hello = $eventos | Where-Object { $_.type -eq "hello" } | Select-Object -First 1
Paso "evento hello" ($null -ne $hello) "usuario $($hello.data.user), canales: $(@($hello.data.topics) -join ', ')"

Write-Host "3) Entrenamiento (admin) mientras se escucha el WebSocket" -ForegroundColor Cyan
$job = Start-Job -ScriptBlock { param($tok)
    Add-Type -AssemblyName System.Net.Http   # el proceso del job no carga este ensamblado por defecto
    $c = New-Object System.Net.Http.HttpClient; $c.Timeout = [TimeSpan]::FromMinutes(10)
    $req = New-Object System.Net.Http.HttpRequestMessage ([System.Net.Http.HttpMethod]::Post), "http://localhost:8080/api/v1/train"
    $req.Headers.Add("Authorization", "Bearer $tok")
    $req.Content = New-Object System.Net.Http.StringContent '{"trees":32,"workers":4}', ([System.Text.Encoding]::UTF8), "application/json"
    $resp = $c.SendAsync($req).GetAwaiter().GetResult()
    [int]$resp.StatusCode
} -ArgumentList $admin
$eventos.Clear()
while ($job.State -eq "Running") { & $recibir 500 }
& $recibir 2500
$codigo = Receive-Job $job
$tr = @($eventos | Where-Object { $_.type -eq "training" })
$prog = @($tr | Where-Object { $_.data.event -eq "progreso" })
$finEv = $tr | Where-Object { $_.data.event -eq "fin" } | Select-Object -First 1
Paso "POST /train" ($codigo -eq 200) "HTTP $codigo"
Paso "progreso por WebSocket" ($prog.Count -gt 0 -and $finEv) ("{0} eventos de progreso de {1} nodos; fin v{2} en {3:N0} ms" -f $prog.Count, (@($prog | ForEach-Object { $_.data.node } | Sort-Object -Unique)).Count, $finEv.data.version, $finEv.data.durationMs)
Paso "métricas por WebSocket" (@($eventos | Where-Object { $_.type -eq "metrics" }).Count -gt 0) "$(@($eventos | Where-Object { $_.type -eq 'metrics' }).Count) fotos del clúster recibidas"
Paso "logs por WebSocket" (@($eventos | Where-Object { $_.type -eq "logs" }).Count -gt 0) "$(@($eventos | Where-Object { $_.type -eq 'logs' }).Count) líneas de log recibidas"

Write-Host "4) Predicción del analista: respuesta, evento en vivo, MongoDB y Redis" -ForegroundColor Cyan
$eventos.Clear()
$anio = 2030 + (Get-Random -Maximum 4)
$p1 = Invoke-Api POST "/api/v1/predict" @{ district = "ATE"; mes = 5; anio = $anio } -Token $ana
$p2 = Invoke-Api POST "/api/v1/predict" @{ district = "ATE"; mes = 5; anio = $anio } -Token $ana
& $recibir 2000
$evp = $eventos | Where-Object { $_.type -eq "prediction" } | Select-Object -First 1
Paso "predicción (clúster)" ($p1.Status -eq 200 -and -not $p1.Json.fromCache) ("{0} {1}/{2}: {3} ({4:P1}); {5} ms" -f $p1.Json.district, $p1.Json.mes, $p1.Json.anio, $p1.Json.topClass, $p1.Json.topProbability, $p1.ClientMs)
Paso "misma predicción (caché Redis)" ($p2.Json.fromCache) ("{0} ms" -f $p2.ClientMs)
Paso "evento prediction en vivo" ($null -ne $evp) "usuario $($evp.data.user), $($evp.data.district)"
$cnt = docker compose exec -T mongo mongosh --quiet -u root -p $envs["MONGO_ROOT_PASSWORD"] --authenticationDatabase admin --eval "db.getSiblingDB('tb2_denuncias').predictions.countDocuments({district:'ATE', anio:$anio})" 2>&1
$n = 0; if ("$cnt" -match "(\d+)\s*$") { $n = [int]$Matches[1] }
Paso "persistido en MongoDB" ($n -ge 2) "predictions con ATE/$anio = $n"
$ttl = docker compose exec -T -e "REDISCLI_AUTH=$($envs['REDIS_PASSWORD'])" redis redis-cli --user tb2app TTL "tb2:pred:v$($p1.Json.modelVersion):ATE:0:5:$anio" 2>&1
$t = -1; if ("$ttl" -match "(-?\d+)\s*$") { $t = [int]$Matches[1] }
Paso "clave en Redis con TTL" ($t -gt 0) "TTL = $t s"
$ws.CloseAsync([System.Net.WebSockets.WebSocketCloseStatus]::NormalClosure, "fin", [Threading.CancellationToken]::None).Wait(3000) | Out-Null

Write-Host "5) Políticas de seguridad" -ForegroundColor Cyan
$v = Invoke-Api GET "/api/v1/districts" -SinToken
Paso "sin token -> 401" ($v.Status -eq 401) "$($v.Status) $($v.Json.code)"
$v = Invoke-Api GET "/api/v1/districts" -Token ($ana.Substring(0, $ana.Length - 2) + "xx")
Paso "firma alterada -> 401" ($v.Status -eq 401) "$($v.Status) $($v.Json.code): $($v.Json.error)"
$v = Invoke-Api POST "/api/v1/train" @{} -Token $ana
Paso "analista entrena -> 403" ($v.Status -eq 403) "$($v.Status) $($v.Json.code)"
$v = Invoke-Api GET "/api/v1/admin/logs" -Token $ana
Paso "analista lee logs -> 403" ($v.Status -eq 403) "$($v.Status) $($v.Json.code)"
$v = Invoke-Api OPTIONS "/api/v1/predict" -SinToken -Headers @{ "Origin" = "http://localhost:8080"; "Access-Control-Request-Method" = "POST" }
Paso "CORS: origen permitido (preflight)" ($v.Status -eq 204 -and $v.Headers["Access-Control-Allow-Origin"] -eq "http://localhost:8080") "$($v.Status), Allow-Origin=$($v.Headers['Access-Control-Allow-Origin'])"
$v = Invoke-Api GET "/api/v1/districts" -Token $ana -Headers @{ "Origin" = "http://sitio-malicioso.example" }
Paso "CORS: origen ajeno -> 403" ($v.Status -eq 403) "$($v.Status) $($v.Json.code)"
$codigos = @()
$ultimo = $null
for ($i = 1; $i -le 11; $i++) { $ultimo = Invoke-Api POST "/api/v1/auth/login" @{ username = "admin"; password = "clave-equivocada-$i" } -SinToken; $codigos += $ultimo.Status }
Paso "login: 10 fallos y el 11.º -> 429" ($ultimo.Status -eq 429 -and $ultimo.Json.code -eq "TOO_MANY_ATTEMPTS") "códigos: $($codigos -join ', '); $($ultimo.Json.error)"
$cuota = @{}
for ($i = 1; $i -le 360; $i++) { $v = Invoke-Api GET "/api/v1/districts" -Token $ana; $k = "$($v.Status) $($v.Json.code)".Trim(); $cuota[$k] = 1 + [int]$cuota[$k] }
Paso "cuota del analista (300/min) -> 429 QUOTA_EXCEEDED" ($cuota.ContainsKey("429 QUOTA_EXCEEDED")) (($cuota.Keys | Sort-Object | ForEach-Object { "HTTP $($_): $($cuota[$_])" }) -join ", ")
$hdr = Invoke-Api GET "/api/v1/health" -SinToken
$xcto = "$($hdr.Headers['X-Content-Type-Options'])"; $xfo = "$($hdr.Headers['X-Frame-Options'])"; $rp = "$($hdr.Headers['Referrer-Policy'])"
Paso "cabeceras de seguridad" ($xcto -eq "nosniff" -and $xfo -eq "DENY" -and $rp -eq "no-referrer") "X-Content-Type-Options=$xcto; X-Frame-Options=$xfo; Referrer-Policy=$rp"

$r.pasos = $pasos
$ok = @($pasos | Where-Object { $_.correcto }).Count
Write-Host ("`n{0} de {1} pasos correctos" -f $ok, $pasos.Count) -ForegroundColor $(if ($ok -eq $pasos.Count) { "Green" } else { "Yellow" })
Save-Json $r "09_e2e_seguridad.json"
Save-Csv $pasos "09_e2e_seguridad.csv"
Write-Host "Nota: el analista quedó con la cuota agotada por un minuto y la IP con el login bloqueado un minuto." -ForegroundColor DarkGray
