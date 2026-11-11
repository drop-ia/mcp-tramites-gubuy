# mcp-tramites-gub-uy

Servidor [MCP](https://modelcontextprotocol.io) en Go para consultar los trámites y servicios del Estado uruguayo.

Fuentes:

| Fuente | Contenido | Origen |
|---|---|---|
| [Catálogo de trámites y servicios del Estado](https://catalogodatos.gub.uy/dataset/agesic-guia-de-tramites) (AGESIC) | Unos 3.500 trámites de 166 organismos ([gub.uy/tramites](https://www.gub.uy/tramites)) | Dataset abierto en CSV, actualizado a diario |
| [Intendencia de Montevideo](https://tramites.montevideo.gub.uy) | Unos 500 trámites publicados, que no están en el catálogo de AGESIC | API JSON:API pública del portal (Drupal) |

La API de la IM no está publicada como dato abierto ni tiene documentación, así que puede cambiar sin aviso. Si falla, el servidor sigue respondiendo con los datos de AGESIC y agrega un aviso.

> **Aviso:** tanto este MCP como la IA pueden cometer errores. La información no es una fuente de verdad: verificá siempre en la ficha oficial del trámite antes de realizarlo. Cada resultado de las herramientas termina con este aviso, y las instrucciones del servidor le piden al modelo que lo repita al usuario.

## Herramientas

| Herramienta | Qué hace |
|---|---|
| `buscar_tramites` | Busca por palabras clave, sin importar tildes. Filtros: `institucion` (nombre parcial o sigla: `BPS`, `MEC`, `IM`), `canal` (`internet` / `persona` / `telefono`), `solo_gratis`, `limite`. |
| `obtener_tramite` | Devuelve la ficha completa por `id`: requisitos, costos, cómo se hace, direcciones, contactos y enlaces. Si un id tiene varias casuísticas, las devuelve todas. Los trámites de la IM usan ids `im-<número>`. |
| `listar_instituciones` | Lista los organismos y cuántos trámites tiene cada uno. |

## Compilar

```sh
go build -o bin/mcp-tramites-gub-uy .
go test ./...
```

## Configurar en un cliente MCP

Claude Code:

```sh
claude mcp add tramites-gub-uy -- /ruta/absoluta/bin/mcp-tramites-gub-uy
```

Claude Desktop y otros clientes (`mcpServers`):

```json
{
  "mcpServers": {
    "tramites-gub-uy": { "command": "/ruta/absoluta/bin/mcp-tramites-gub-uy" }
  }
}
```

## Modo HTTP y Docker

Con `-http :8080` (o la variable `MCP_HTTP_ADDR`) el servidor atiende MCP por HTTP en `/mcp`, sin sesión porque las herramientas solo leen, en lugar de stdio. También expone `GET /health` para el healthcheck.

```sh
docker build -t mcp-tramites-gub-uy .
docker run -p 8080:8080 -v tramites-cache:/data mcp-tramites-gub-uy
# endpoint: http://localhost:8080/mcp
```

La imagen pesa unos 8 MB, corre con un usuario sin privilegios y guarda la caché en el volumen `/data` (`TRAMITES_CACHE_DIR`), así que un redeploy arranca sin volver a descargar.

### Despliegue en Dokploy (tramites.dropia.uy)

Corre en **https://tramites.dropia.uy/mcp**. Está en Dokploy Cloud (`app.dokploy.com`), proyecto `MCP Trámites`, environment `production`, servidor `OVH - dropia - I` (`144.217.88.113`). Se desplegó por primera vez el 10 de noviembre de 2026.

| Elemento | Valor |
|---|---|
| Aplicación | `mcp-tramites` (`applicationId` `fRS84sTm4BsbXiQ_adogd`, nombre interno `mcp-tramites-a1h5dc`) |
| Origen | GitHub `drop-ia/mcp-tramites-gubuy`, rama `main`, por la app `Dokploy-DropIA`. Build type **Dockerfile** |
| Dominio | `tramites.dropia.uy` → puerto `8080`, HTTPS con Let's Encrypt. El DNS es un registro A a `144.217.88.113` |
| Volumen | `mcp-tramites-cache` montado en `/data` (caché de los catálogos) |
| Variables | Ninguna |

**Auto-deploy:** cada push a `main` construye y despliega.

Para redesplegar a mano (la API de Dokploy pide un `User-Agent`; sin él, Cloudflare responde 403):

```sh
curl -X POST -H "x-api-key: $DOKPLOY_TOKEN" -H "User-Agent: curl/8.0" -H "Content-Type: application/json" \
  -d '{"applicationId":"fRS84sTm4BsbXiQ_adogd"}' https://app.dokploy.com/api/application.deploy
```

## Datos y caché

- Al arrancar, cada fuente se descarga en segundo plano y se guarda en `$(os.UserCacheDir)/mcp-tramites-gub-uy/` (en macOS: `~/Library/Caches/...`): `tramites.csv` (unos 10 MB) y `montevideo-v2.json`. Pasadas 24 h se vuelven a descargar, sin dejar de responder con la copia anterior.
- La IM se recorre en unas 11 páginas, y la primera descarga puede tardar más de un minuto. Una consulta espera a lo sumo 20 s; si no llega, responde sin la IM y con un aviso.
- Si una descarga falla, se sigue usando la última copia en disco, aunque tenga más de 24 h.
- Para usar otras URLs, definí `TRAMITES_CSV_URL` (CSV de AGESIC) o `TRAMITES_IM_URL` (base del portal de la IM).
- Los logs van a stderr; stdout queda reservado para el protocolo MCP.

## Limitaciones del dataset

- No todos los organismos publican en el catálogo de AGESIC. La Intendencia de Montevideo se agrega aparte; de las demás intendencias solo están las 11 que publican ahí.
- AGESIC informa el costo como `tiene_costo` más una unidad y un valor (U.R., U.I., $), a menudo vacíos o `Sin definir`. La IM lo informa como texto libre, y solo los que empiezan con "No tiene costo" cuentan como gratuitos.
- Los trámites de la IM no traen canales de atención: el campo de origen no coincide con lo que muestra el portal. Por eso el filtro `canal` los excluye; el detalle está en la sección "¿Cómo se hace?".
