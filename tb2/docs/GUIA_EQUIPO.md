# Guía para el equipo: levantar, probar y auditar el TB2

Para Karim y Tomás (y cualquiera que revise el proyecto). Lleva unos 15 minutos la primera vez.

## 1. Qué necesitas

- Windows con **Docker Desktop** abierto (motor en verde) y **Git**.
- No hace falta instalar Go ni Node: todo se compila dentro de Docker.
- El puerto **8080** libre. Si tienes levantada la PC4, el script la detiene solo.
- Unos 4 GB de RAM libres y 3 GB de disco para las imágenes.

## 2. Descargar y levantar

```powershell
git clone https://github.com/jotabe12/CC65-TB2-Grupo3-RandomForest-Denuncias.git
cd CC65-TB2-Grupo3-RandomForest-Denuncias\tb2
Set-ExecutionPolicy -Scope Process Bypass      # solo para esta ventana de PowerShell
.\scripts\01_levantar.ps1
```

- El CSV de denuncias está en la raíz del repositorio y el Excel del INEI en `tb2\data\`. El script los usa solos.
- **No necesitas el `.env` de nadie.** La primera vez, el script lo crea a partir de `.env.example` con claves aleatorias para JWT, MongoDB y Redis. El `.env` no se sube a GitHub, a propósito: tiene secretos.
- La primera vez tarda unos minutos, porque descarga imágenes. Termina cuando muestra «Listo … modelo vN».

Al terminar:

| Qué | Dónde |
|---|---|
| Aplicación web | http://localhost:8080 |
| Swagger (documentación de la API) | http://localhost:8080/swagger |
| Usuario administrador | `admin` / `Admin#TB2-2026` |
| Usuario analista | `analista` / `Analista#TB2-2026` |

Para apagar: `.\scripts\99_apagar.ps1`. Con `-Borrar` también se borra la base de datos.

## 3. Recorrido por la aplicación (5 minutos)

1. **Entra como `analista`.** El menú solo muestra Planificación y Evaluación; el analista no ve el entrenamiento ni el clúster.
2. **Impacto social.** Arriba está la respuesta a la pregunta del proyecto. Cambia el mes (enero a septiembre de 2026 se comparan con lo real) y el orden por «tasa por 100 mil».
3. **Plan de patrullaje.** Es la hoja de trabajo para la comisaría. Prueba el botón «Imprimir plan».
4. **Predicción por distrito.** Elige un distrito, mes y año y pulsa Predecir. Si repites la consulta, la respuesta sale de la caché de Redis (etiqueta «desde caché Redis»).
5. **Evaluación del modelo.** Exactitud, top-3, F1 por modalidad, matriz de confusión e historial.
6. **Abre una ventana de incógnito y entra como `admin`.** Ve a Entrenamiento y pulsa Entrenar: las barras de cada nodo avanzan en vivo. En **Clúster y logs** se ven la CPU, la latencia y los logs. Si en la otra ventana el analista predice algo, aparece al instante en el feed del admin.
7. **Caída de un nodo** (opcional, buena escena para el video): `docker stop tb2-grupo3-ml-node-2-1`. El panel marca 2 de 3 y la predicción sigue funcionando con 21 árboles. Con `docker start tb2-grupo3-ml-node-2-1` vuelve en unos segundos.

## 4. Scripts de prueba (carpeta `tb2\scripts`)

Cada uno imprime un resumen y guarda resultados en `tb2\evidencias\`. **No hace falta correrlos para auditar**: los resultados ya están en el repositorio. Si los corres, los archivos de evidencia cambiarán con tus tiempos; no los subas.

| Script | Qué prueba | Tiempo |
|---|---|---|
| `02_pruebas_api` | Login, predicción con y sin caché, validaciones de error | 10 s |
| `03_resiliencia` | Apaga y prende un nodo mientras se consulta | 20 s |
| `04_evidencias_bd` | Redes de Docker, MongoDB, Redis y accesos indebidos bloqueados | 30 s |
| `05_go_test` | `go vet` y `go test -race` de todo el código | 5 min |
| `06_speedup` | Entrenamiento con 1-3 nodos y 1-8 goroutines | 3 min |
| `07_metricas_modelo` | Exactitud, F1 y matriz con distintas variables | 2 min |
| `08_carga_api` | 10, 50 y 100 clientes simultáneos (< 100 ms) | 1 min |
| `09_e2e_seguridad` | Flujo completo + JWT, roles, CORS, cuotas | 1 min |
| `10_impacto` | Respuesta a la pregunta de impacto, mes a mes | 1 min |

> El 09 deja el login bloqueado y la cuota del analista agotada **un minuto**: espera ese minuto antes de entrar a la web.

## 5. Qué auditar

**Informe** (`1ACC0065_TB2_202620_Informe_U<código>.docx`, uno por integrante):

- [ ] Carátula: tu nombre, código y carrera (Ciencias de la Computación).
- [ ] Las cifras de las secciones f.14 y del resumen coinciden con `tb2\evidencias\*.txt` / `*.csv` (por ejemplo `07_metricas.txt`, `10_impacto.txt`, `08_carga_api.csv`).
- [ ] Tu fila del Anexo D (reporte de participación) describe bien lo que hiciste. Si quieres cambiar algo, dilo y se ajusta.
- [ ] Ortografía, figuras legibles, índice con números de página.

**Repositorio y Kanban:**

- [ ] https://github.com/jotabe12/CC65-TB2-Grupo3-RandomForest-Denuncias: ramas, Pull Requests #28 a #34 y etiquetas v1.0-pc3, v2.0-pc4, v3.0-tb2.
- [ ] https://github.com/users/jotabe12/projects/1: tablero con la columna «Responsable».
- [ ] Acepta la invitación de colaborador que llegó a tu correo o a https://github.com/notifications.

**Aplicación:** sigue el recorrido de la sección 3 y anota cualquier cosa rara (texto, número que no cuadre, error).

Las observaciones se dejan como comentario en la issue **#25 (Revisión cruzada contra la rúbrica)** o por el chat del grupo, con captura si es posible.

## 6. Problemas comunes

| Síntoma | Solución |
|---|---|
| «no se puede cargar … la ejecución de scripts está deshabilitada» | `Set-ExecutionPolicy -Scope Process Bypass` en esa ventana |
| «docker compose up falló» | Abre Docker Desktop y espera a que el motor esté en verde |
| El puerto 8080 está ocupado | Apaga lo que lo use (otra app o la PC4: `pc4-mejora\scripts\99_apagar.ps1`) |
| «demasiados intentos fallidos» o «cuota superada» | Espera 1 minuto: son las políticas de seguridad funcionando |
| La página dice «Tiempo real desconectado» | El coordinador se reinició; recarga la página |
