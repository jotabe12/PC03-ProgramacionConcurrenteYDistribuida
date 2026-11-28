# Speedup y eficiencia del entrenamiento distribuido.
# Combina N nodos (1, 2, 3) x W goroutines por nodo (1, 2, 4, 8), con 3 repeticiones cada uno.
# Mide el tiempo de pared en el coordinador (fan-out + fan-in) y el entrenamiento del nodo más lento.
# Al terminar vuelve a entrenar el modelo por defecto (tb2, 32 árboles, todos los nodos).
param([int]$Arboles = 64, [int]$Repeticiones = 3)
. "$PSScriptRoot\_comun.ps1"
Connect-Api "admin" | Out-Null
$entorno = Get-Entorno
Write-Host ("Speedup con {0} árboles, {1} repeticiones. Equipo: {2} ({3} hilos), {4}" -f $Arboles, $Repeticiones, $entorno.cpu, $entorno.hilosLogicos, $entorno.dockerVM) -ForegroundColor Cyan

$nodos = @(1, 2, 3)
$workers = @(1, 2, 4, 8)
# Calentamiento: cada nodo carga y desagrega su shard (no se mide).
foreach ($n in $nodos) { Invoke-Train @{ trees = 3; nodes = $n; workers = 8 } | Out-Null }

$crudo = @()
for ($rep = 1; $rep -le $Repeticiones; $rep++) {
    foreach ($n in $nodos) {
        foreach ($w in $workers) {
            $t = Invoke-Train @{ trees = $Arboles; nodes = $n; workers = $w }
            $j = $t.Json
            $crudo += [pscustomobject][ordered]@{
                repeticion = $rep; nodos = $n; goroutinesPorNodo = $w; goroutinesTotales = $n * $w
                paredMs = [math]::Round($j.durationMs, 1); entrenamientoMaxMs = [math]::Round($j.maxTrainMs, 1)
                denunciasPorNodo = ($j.nodes | ForEach-Object { $_.result.trainRecords }) -join "/"
                arbolesPorNodo = ($j.nodes | ForEach-Object { $_.result.trees }) -join "/"
                exactitud = [math]::Round($j.evaluation.accuracy, 4)
            }
            Write-Host ("   rep {0}: {1} nodo(s) x {2} goroutines -> pared {3:N0} ms, nodo más lento {4:N0} ms" -f $rep, $n, $w, $j.durationMs, $j.maxTrainMs)
        }
    }
}

function Mediana([double[]]$v) { $o = $v | Sort-Object; $o[[int][math]::Floor(($o.Count - 1) / 2)] }
$baseRuns = $crudo | Where-Object { $_.nodos -eq 1 -and $_.goroutinesPorNodo -eq 1 }
$t1 = Mediana ($baseRuns | ForEach-Object { $_.paredMs })
$t1e = Mediana ($baseRuns | ForEach-Object { $_.entrenamientoMaxMs })
$resumen = @()
foreach ($n in $nodos) {
    foreach ($w in $workers) {
        $g = $crudo | Where-Object { $_.nodos -eq $n -and $_.goroutinesPorNodo -eq $w }
        $tp = Mediana ($g | ForEach-Object { $_.paredMs })
        $te = Mediana ($g | ForEach-Object { $_.entrenamientoMaxMs })
        $p = $n * $w
        $s = $t1 / $tp
        $serial = $null
        if ($p -gt 1 -and $s -gt 0) { $serial = [math]::Round((($p / $s) - 1) / ($p - 1), 4) }   # Karp-Flatt
        $resumen += [pscustomobject][ordered]@{
            nodos = $n; goroutinesPorNodo = $w; goroutinesTotales = $p
            paredMedianaMs = [math]::Round($tp, 1); entrenamientoMaxMedianoMs = [math]::Round($te, 1)
            speedup = [math]::Round($s, 2); speedupEntrenamiento = [math]::Round($t1e / $te, 2)
            eficiencia = [math]::Round($s / $p, 3); fraccionSerialKarpFlatt = $serial
        }
    }
}
$resumen | Format-Table -AutoSize

$mejor = $resumen | Sort-Object paredMedianaMs | Select-Object -First 1
Write-Host ("Mejor: {0} nodos x {1} goroutines, speedup {2}x ({3:N0} ms frente a {4:N0} ms)" -f $mejor.nodos, $mejor.goroutinesPorNodo, $mejor.speedup, $mejor.paredMedianaMs, $t1) -ForegroundColor Green

Write-Host "Restaurando el modelo por defecto (tb2, 32 árboles, todos los nodos)..." -ForegroundColor Cyan
$final = Invoke-Train @{}
Write-Host ("   modelo v{0}: exactitud {1:P2}" -f $final.Json.version, $final.Json.evaluation.accuracy)

Save-Json ([ordered]@{ fecha = (Get-Date).ToString("yyyy-MM-ddTHH:mm:ss"); entorno = $entorno; arboles = $Arboles; repeticiones = $Repeticiones
        nota = "speedup = T(1 nodo, 1 goroutine) / T(N, W) con la mediana del tiempo de pared en el coordinador; fracción serial estimada con la métrica de Karp-Flatt"
        resumen = $resumen; crudo = $crudo }) "06_speedup.json"
Save-Csv $resumen "06_speedup.csv"
