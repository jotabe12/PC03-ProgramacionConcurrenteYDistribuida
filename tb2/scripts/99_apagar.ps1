# Detiene el stack del TB2. Con -Borrar también elimina el volumen de MongoDB (usuarios y registros).
param([switch]$Borrar)
. "$PSScriptRoot\_comun.ps1"
if ($Borrar) { docker compose down -v } else { docker compose down }
