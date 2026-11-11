package main

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Fixture con el formato real: BOM, columnas sin comillas, campo multilínea e id repetido (casuística).
const csvPrueba = "\xef\xbb\xbf" + `"id","nombre_tramite","institucion_oid","institucion_nombre","institucion_padre_organizacional_oid","institucion_padre_organizacional_nombre",creado,actualizado,"url","ques_es","casuistica","requisitos_generales","tiene_costo","unidad","valor","canales_de_atencion","internet_url_del_tramite"
"4","Información sobre archivos históricos","1.1","Archivo General de la Nación","1","Ministerio de Educación y Cultura",14/10/2020,06/11/2026,"https://www.gub.uy/tramites/informacion-archivos-historicos","Servicio de consulta de documentos históricos.","","Agenda previa.
Solicitar agenda por correo | Documento de identidad","","","","internet, persona","mailto:sala@agn.gub.uy"
"10","Renovación de licencia de conducir","2.1","Intendencia de Montevideo","2","Intendencia de Montevideo",01/01/2021,01/02/2026,"https://www.gub.uy/tramites/renovacion-licencia","Renovar la libreta de conducir.","","Cédula","1","U.R.","2","persona",""
"261-1","Permiso para menor de edad - Menor autorizado","3.1","Instituto del Niño y Adolescente","3","INAU",22/12/2025,22/12/2025,"https://www.gub.uy/tramites/permiso-menor","Viaje de menores.","1","","","","","persona",""
"261-1","Permiso para menor de edad - Cuando uno de los padres","3.1","Instituto del Niño y Adolescente","3","INAU",22/12/2025,22/12/2025,"https://www.gub.uy/tramites/permiso-menor-2","Viaje de menores.","1","","","","","persona",""
`

func datosPrueba(t *testing.T) *Datos {
	t.Helper()
	d, err := Parsear(strings.NewReader(csvPrueba))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestParsear(t *testing.T) {
	d := datosPrueba(t)
	if len(d.Tramites) != 4 {
		t.Fatalf("esperaba 4 trámites, hay %d", len(d.Tramites))
	}
	if got := d.PorID["4"][0].RequisitosGenerales; !strings.Contains(got, "\nSolicitar agenda") {
		t.Errorf("campo multilínea mal parseado: %q", got)
	}
	if n := len(d.PorID["261-1"]); n != 2 {
		t.Errorf("id con casuísticas: esperaba 2 fichas, hay %d", n)
	}
	if _, err := Parsear(strings.NewReader("a,b\n1,2\n")); err == nil {
		t.Error("esperaba error con columnas faltantes")
	}
}

func TestBuscar(t *testing.T) {
	d := datosPrueba(t)
	casos := []struct {
		nombre string
		f      FiltrosBusqueda
		ids    []string
	}{
		{"sin tildes", FiltrosBusqueda{Consulta: "archivos historicos"}, []string{"4"}},
		{"con tildes y mayúsculas", FiltrosBusqueda{Consulta: "LICENCIA de Conducir"}, []string{"10"}},
		{"prefijo", FiltrosBusqueda{Consulta: "licen"}, []string{"10"}},
		{"todos los términos", FiltrosBusqueda{Consulta: "licencia historicos"}, nil},
		{"por institución", FiltrosBusqueda{Institucion: "intendencia"}, []string{"10"}},
		{"por organismo padre", FiltrosBusqueda{Institucion: "INAU"}, []string{"261-1", "261-1"}},
		{"por sigla derivada", FiltrosBusqueda{Institucion: "MEC"}, []string{"4"}},
		{"sigla no matchea parcial", FiltrosBusqueda{Institucion: "me"}, nil},
		{"filtro corto no matchea dentro de palabras", FiltrosBusqueda{Institucion: "ion"}, nil},
		{"frase de varias palabras", FiltrosBusqueda{Institucion: "general de la nacion"}, []string{"4"}},
		{"canal", FiltrosBusqueda{Consulta: "", Institucion: "archivo", Canal: "internet"}, []string{"4"}},
		{"canal sin match", FiltrosBusqueda{Consulta: "licencia", Canal: "internet"}, nil},
		{"solo gratis", FiltrosBusqueda{Consulta: "licencia", SoloGratis: true}, nil},
	}
	for _, c := range casos {
		res, total := Buscar(d, c.f)
		var ids []string
		for _, r := range res {
			ids = append(ids, r.ID)
		}
		if strings.Join(ids, ",") != strings.Join(c.ids, ",") || total != len(c.ids) {
			t.Errorf("%s: obtuve %v (total %d), esperaba %v", c.nombre, ids, total, c.ids)
		}
	}
}

func TestListarInstitucionesMismoNombre(t *testing.T) {
	csv := `"id","nombre_tramite","institucion_oid","institucion_nombre","institucion_padre_organizacional_oid","institucion_padre_organizacional_nombre"
"1","A","3.14.1","Intendencia","3.14","RIVERA"
"2","B","3.10.1","Intendencia","3.10","MONTEVIDEO"
"3","C","3.10.1","Intendencia","3.10","MONTEVIDEO"
`
	d, err := Parsear(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	if l := ListarInstituciones(d, ""); len(l) != 2 {
		t.Fatalf("esperaba 2 intendencias distintas, hay %+v", l)
	}
	if l := ListarInstituciones(d, "montevideo"); len(l) != 1 || l[0].Tramites != 2 || l[0].Padre != "MONTEVIDEO" {
		t.Errorf("filtro por departamento: %+v", l)
	}
}

func TestFicha(t *testing.T) {
	f := FormatearFicha(datosPrueba(t).PorID["10"][0])
	for _, want := range []string{"# Renovación de licencia de conducir", "tiene costo: U.R. 2", "## Requisitos\nCédula"} {
		if !strings.Contains(f, want) {
			t.Errorf("la ficha no contiene %q:\n%s", want, f)
		}
	}
}

// Dos páginas de la API JSON:API de la IM; el enlace "next" viene con http:// como en producción.
const imPagina1 = `{"data":[{"type":"node--tramite","id":"u1","attributes":{"drupal_internal__nid":24588,"title":"Limpieza de boca de tormenta","created":"2012-05-31T15:38:09+00:00","changed":{"value":"2026-04-29T14:13:03+00:00"},"path":{"alias":"/tramites-y-tributos/solicitud/limpieza-boca-de-tormenta"},"field_tra_descripcion":"Solicitá la limpieza de una boca de tormenta tapada.\r\n","field_tra_requisitos":{"value":"x","processed":"<p>En el <a href=\"https://formularios.montevideo.gub.uy/f\">formulario web</a> debe:</p><ul><li data-x=\"1\">Indicar la dirección.</li><li>Adjuntar foto&nbsp;opcional.</li></ul><p>&nbsp;</p>"},"field_tra_costo":{"processed":"<p>No tiene costo</p>"},"field_tra_procedimiento":{"processed":"<h3>Whatsapp</h3><p>Ver <a href=\"/atencion\">atención</a></p>"},"field_tra_aclaraciones":null},"relationships":{"field_cmp_dep_responsable":{"data":{"type":"taxonomy_term--dependencia","id":"d1"}},"field_cmp_categoria":{"data":[{"type":"taxonomy_term--categoria_tramite","id":"c1"}]}}}],
"included":[{"type":"taxonomy_term--dependencia","id":"d1","attributes":{"name":"Servicio de Saneamiento"}},{"type":"taxonomy_term--categoria_tramite","id":"c1","attributes":{"name":"Solicitud"}}],
"links":{"next":{"href":"http://example.invalid/jsonapi/node/tramite?filter%5Bstatus%5D=1&sort=drupal_internal__nid&page%5Boffset%5D=50&page%5Blimit%5D=50"}}}`

const imPagina2 = `{"data":[{"type":"node--tramite","id":"u2","attributes":{"drupal_internal__nid":30,"title":"Habilitación de piscinas","created":"2020-01-01T00:00:00+00:00","changed":{"value":"2026-01-02T00:00:00+00:00"},"path":{"alias":"/tramites-y-tributos/certificado/piscinas"},"field_tra_descripcion":"Certificado higiénico.","field_tra_requisitos":null,"field_tra_costo":{"processed":"<p>$ 1.552</p>"},"field_tra_procedimiento":null},"relationships":{"field_cmp_dep_responsable":{"data":null},"field_cmp_categoria":{"data":[]}}}],"included":[],"links":{}}`

type servidorPrueba struct {
	srv        *httptest.Server
	pedidosCSV atomic.Int32
	pedidosIM  atomic.Int32
	demoraIM   time.Duration
	falloIM    atomic.Bool
}

func nuevoServidorPrueba(t *testing.T, demoraIM time.Duration) *servidorPrueba {
	sp := &servidorPrueba{demoraIM: demoraIM}
	sp.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/tramites.csv":
			sp.pedidosCSV.Add(1)
			io.WriteString(w, csvPrueba)
		case "/jsonapi/node/tramite":
			sp.pedidosIM.Add(1)
			time.Sleep(sp.demoraIM)
			if sp.falloIM.Load() {
				http.Error(w, "caído", http.StatusBadGateway)
				return
			}
			if r.URL.Query().Get("sort") != "drupal_internal__nid" {
				t.Errorf("falta un orden estable para paginar: %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("filter[status]") != "1" {
				t.Errorf("falta filtrar por publicados: %s", r.URL.RawQuery)
			}
			if r.URL.Query().Get("page[offset]") == "50" {
				io.WriteString(w, imPagina2)
			} else {
				io.WriteString(w, imPagina1)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(sp.srv.Close)
	return sp
}

func (sp *servidorPrueba) catalogo(t *testing.T, dir string, espera time.Duration) *Catalogo {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &Catalogo{
		fuentes: []*fuente{
			fuenteAGESIC(sp.srv.URL+"/tramites.csv", filepath.Join(dir, "tramites.csv"), sp.srv.Client(), log),
			fuenteMontevideo(sp.srv.URL, filepath.Join(dir, "montevideo.json"), sp.srv.Client(), log),
		},
		espera: espera,
	}
}

func TestMontevideo(t *testing.T) {
	sp := nuevoServidorPrueba(t, 0)
	d, err := sp.catalogo(t, t.TempDir(), 5*time.Second).Obtener(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Avisos) != 0 || len(d.Tramites) != 6 {
		t.Fatalf("esperaba 4 de AGESIC + 2 de la IM sin avisos: %d %v", len(d.Tramites), d.Avisos)
	}
	im := d.PorID["im-24588"][0]
	comprobar := map[string][2]string{
		"Institucion": {im.Institucion, "Servicio de Saneamiento"},
		"Padre":       {im.InstitucionPadre, "Intendencia de Montevideo"},
		"URL":         {im.URL, sp.srv.URL + "/tramites-y-tributos/solicitud/limpieza-boca-de-tormenta"},
		"Actualizado": {im.Actualizado, "29/04/2026"},
		"QueEs":       {im.QueEs, "Solicitá la limpieza de una boca de tormenta tapada."},
		"Requisitos":  {im.RequisitosGenerales, "En el formulario web (https://formularios.montevideo.gub.uy/f) debe:\n- Indicar la dirección.\n- Adjuntar foto opcional."},
		"ComoSeHace":  {im.ComoSeHace, "Whatsapp\nVer atención (" + sp.srv.URL + "/atencion)"},
		"Categoria":   {im.CategoriasDeOrganismo, "Solicitud"},
		"TieneCosto":  {im.TieneCosto, ""},
	}
	for campo, v := range comprobar {
		if v[0] != v[1] {
			t.Errorf("%s = %q, esperaba %q", campo, v[0], v[1])
		}
	}
	piscinas := d.PorID["im-30"][0]
	if piscinas.TieneCosto != "1" || piscinas.Institucion != "Intendencia de Montevideo" {
		t.Errorf("trámite sin dependencia y con costo mal convertido: %+v", piscinas)
	}

	// Búsquedas que cruzan fuentes.
	// 2 de la IM + la fila de AGESIC del fixture cuya institución es "Intendencia de Montevideo".
	if res, _ := Buscar(d, FiltrosBusqueda{Institucion: "IM"}); len(res) != 3 {
		t.Errorf("sigla IM: %d resultados", len(res))
	}
	if res, _ := Buscar(d, FiltrosBusqueda{Consulta: "piscinas", SoloGratis: true}); len(res) != 0 {
		t.Error("solo_gratis debería excluir trámites IM con costo")
	}
	if res, _ := Buscar(d, FiltrosBusqueda{Consulta: "boca tormenta", SoloGratis: true}); len(res) != 1 {
		t.Error("solo_gratis debería incluir 'No tiene costo'")
	}
	ficha := FormatearFicha(im)
	for _, want := range []string{"## Costo\nNo tiene costo", "## ¿Cómo se hace?\nWhatsapp", "**Fuente:** Intendencia de Montevideo"} {
		if !strings.Contains(ficha, want) {
			t.Errorf("la ficha IM no contiene %q:\n%s", want, ficha)
		}
	}
}

func TestFuenteLentaNoBloquea(t *testing.T) {
	sp := nuevoServidorPrueba(t, 2*time.Second)
	cat := sp.catalogo(t, t.TempDir(), 300*time.Millisecond)
	inicio := time.Now()
	d, err := cat.Obtener(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(inicio) > time.Second {
		t.Errorf("Obtener esperó %v; debía cortar en ~300ms", time.Since(inicio))
	}
	if len(d.Tramites) != 4 || len(d.Avisos) != 1 || !strings.Contains(d.Avisos[0], "todavía se están descargando") {
		t.Fatalf("esperaba sólo AGESIC con aviso: %d %v", len(d.Tramites), d.Avisos)
	}
	// Al terminar la descarga, los trámites de la IM aparecen.
	time.Sleep(4500 * time.Millisecond)
	if d, _ = cat.Obtener(context.Background()); len(d.Tramites) != 6 || len(d.Avisos) != 0 {
		t.Errorf("después de cargar: %d %v", len(d.Tramites), d.Avisos)
	}
}

func TestFuenteCaidaYCache(t *testing.T) {
	dir := t.TempDir()
	sp := nuevoServidorPrueba(t, 0)
	sp.falloIM.Store(true)
	d, err := sp.catalogo(t, dir, 5*time.Second).Obtener(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Tramites) != 4 || len(d.Avisos) != 1 || !strings.Contains(d.Avisos[0], "No se pudieron obtener") {
		t.Fatalf("esperaba aviso de fuente caída: %v", d.Avisos)
	}

	// Con la IM de nuevo arriba, un catálogo nuevo descarga y deja ambas cachés en disco.
	sp.falloIM.Store(false)
	if d, _ = sp.catalogo(t, dir, 5*time.Second).Obtener(context.Background()); len(d.Tramites) != 6 {
		t.Fatalf("esperaba 6 trámites, hay %d (%v)", len(d.Tramites), d.Avisos)
	}
	csvAntes, imAntes := sp.pedidosCSV.Load(), sp.pedidosIM.Load()

	// Un tercer catálogo (otro proceso) arranca desde la caché vigente sin descargar nada.
	if d, _ = sp.catalogo(t, dir, 5*time.Second).Obtener(context.Background()); len(d.Tramites) != 6 {
		t.Fatalf("desde caché: %d", len(d.Tramites))
	}
	if sp.pedidosCSV.Load() != csvAntes || sp.pedidosIM.Load() != imAntes {
		t.Error("con caché vigente no debería descargar")
	}

	// Con todo caído y caché vencida, se sirve la caché vieja.
	sp.srv.Close()
	viejo := time.Now().Add(-48 * time.Hour)
	for _, f := range []string{"tramites.csv", "montevideo.json"} {
		os.Chtimes(filepath.Join(dir, f), viejo, viejo)
	}
	if d, err = sp.catalogo(t, dir, 5*time.Second).Obtener(context.Background()); err != nil || len(d.Tramites) != 6 {
		t.Fatalf("fallback a caché vencida: %v %d", err, len(d.Tramites))
	}
}

// Cliente MCP real conectado en memoria al servidor.
func TestServidorMCP(t *testing.T) {
	sp := nuevoServidorPrueba(t, 0)
	cat := sp.catalogo(t, t.TempDir(), 5*time.Second)
	ctx := context.Background()
	tServidor, tCliente := mcp.NewInMemoryTransports()
	if _, err := nuevoServidor(cat).Connect(ctx, tServidor, nil); err != nil {
		t.Fatal(err)
	}
	sesion, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, tCliente, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sesion.Close()

	herramientas, err := sesion.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(herramientas.Tools) != 3 {
		t.Fatalf("esperaba 3 herramientas, hay %d", len(herramientas.Tools))
	}

	llamar := func(nombre string, args map[string]any) (string, bool) {
		t.Helper()
		r, err := sesion.CallTool(ctx, &mcp.CallToolParams{Name: nombre, Arguments: args})
		if err != nil {
			t.Fatalf("%s: %v", nombre, err)
		}
		return r.Content[0].(*mcp.TextContent).Text, r.IsError
	}

	if txt, _ := llamar("buscar_tramites", map[string]any{"consulta": "licencia"}); !strings.Contains(txt, "id: 10") {
		t.Errorf("buscar_tramites: %s", txt)
	}
	for h, args := range map[string]map[string]any{
		"buscar_tramites":      {"consulta": "licencia"},
		"obtener_tramite":      {"id": "10"},
		"listar_instituciones": {},
	} {
		if txt, _ := llamar(h, args); !strings.HasSuffix(txt, avisoErrores) {
			t.Errorf("%s no termina con el aviso de errores: %s", h, txt)
		}
	}
	if txt, _ := llamar("buscar_tramites", map[string]any{"consulta": "tormenta"}); !strings.Contains(txt, "id: im-24588") || !strings.Contains(txt, "Costo: No tiene costo") {
		t.Errorf("buscar_tramites IM: %s", txt)
	}
	if txt, _ := llamar("obtener_tramite", map[string]any{"id": "261-1"}); strings.Count(txt, "# Permiso") != 2 {
		t.Errorf("obtener_tramite debería devolver las 2 casuísticas: %s", txt)
	}
	if txt, esError := llamar("obtener_tramite", map[string]any{"id": "nope"}); !esError || !strings.Contains(txt, "no existe") {
		t.Errorf("id inexistente debería ser error: %v %s", esError, txt)
	}
	if _, esError := llamar("buscar_tramites", map[string]any{"consulta": ""}); !esError {
		t.Error("consulta vacía sin institución debería ser error")
	}
	if txt, _ := llamar("listar_instituciones", map[string]any{"filtro": "montevideo"}); !strings.Contains(txt, "Servicio de Saneamiento (Intendencia de Montevideo): 1") {
		t.Errorf("listar_instituciones: %s", txt)
	}
	if n := sp.pedidosCSV.Load(); n != 1 {
		t.Errorf("esperaba 1 descarga del CSV, hubo %d", n)
	}
	if n := sp.pedidosIM.Load(); n != 2 {
		t.Errorf("esperaba 2 páginas de la IM, hubo %d", n)
	}
}

// Modo HTTP: cliente MCP real contra /mcp, más /health y la raíz.
func TestServidorHTTP(t *testing.T) {
	sp := nuevoServidorPrueba(t, 0)
	web := httptest.NewServer(manejadorHTTP(nuevoServidor(sp.catalogo(t, t.TempDir(), 5*time.Second))))
	defer web.Close()

	for ruta, quiere := range map[string]string{"/health": "ok", "/": "Endpoint MCP: /mcp"} {
		resp, err := http.Get(web.URL + ruta)
		if err != nil {
			t.Fatal(err)
		}
		cuerpo, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(cuerpo), quiere) {
			t.Errorf("%s: %d %q", ruta, resp.StatusCode, cuerpo)
		}
	}
	if resp, _ := http.Get(web.URL + "/otra"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("/otra debería ser 404, fue %d", resp.StatusCode)
	}

	ctx := context.Background()
	sesion, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).
		Connect(ctx, &mcp.StreamableClientTransport{Endpoint: web.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sesion.Close()
	r, err := sesion.CallTool(ctx, &mcp.CallToolParams{Name: "buscar_tramites", Arguments: map[string]any{"consulta": "tormenta"}})
	if err != nil {
		t.Fatal(err)
	}
	if txt := r.Content[0].(*mcp.TextContent).Text; !strings.Contains(txt, "id: im-24588") || !strings.HasSuffix(txt, avisoErrores) {
		t.Errorf("buscar_tramites por HTTP: %s", txt)
	}
}

func TestSinRepetidos(t *testing.T) {
	ts := []*Tramite{{ID: "im-1", Nombre: "a"}, {ID: "im-2"}, {ID: "im-1", Nombre: "b"}, {ID: "im-3"}, {ID: "im-2"}}
	got := sinRepetidos(ts)
	if len(got) != 3 || got[0].Nombre != "a" || got[1].ID != "im-2" || got[2].ID != "im-3" {
		t.Errorf("sinRepetidos = %+v", got)
	}
}

func TestSinonimos(t *testing.T) {
	cabecera := strings.SplitN(csvPrueba, "\n", 2)[0]
	csv := cabecera + `
"6538","Documento Nacional de Identidad - Renovación","4.1","Dirección Nacional de Identificación Civil","4","Ministerio del Interior",01/01/2024,01/01/2026,"https://www.gub.uy/tramites/documento-nacional-identidad-renovacion","Es el trámite para renovar el Documento Nacional de Identidad.","","","","","","persona",""
"5167","Certificado migratorio para renovación de documento de identidad","5.1","Dirección Nacional de Migración","4","Ministerio del Interior",01/01/2024,01/01/2026,"https://www.gub.uy/tramites/certificado-migratorio","Necesario para renovar su cédula de identidad uruguaya.","","","","","","internet",""
"6987","Reempadronamiento de vehículos provenientes de otros departamentos","6.1","Intendencia de Canelones","6","Intendencia de Canelones",01/01/2024,01/01/2026,"https://www.gub.uy/tramites/reempadronamiento","Vehículos empadronados en otro departamento.","","","1","$","4630","persona",""
`
	d, err := Parsear(strings.NewReader(csv))
	if err != nil {
		t.Fatal(err)
	}
	casos := []struct {
		consulta string
		primero  string
		total    int
	}{
		{"renovar cédula de identidad", "6538", 2},
		{"renovar CI", "6538", 2},
		{"DNI", "6538", 2},
		{"radicación de vehículo canelones", "6987", 1},
		{"traslado vehiculo", "6987", 1},
	}
	for _, c := range casos {
		res, total := Buscar(d, FiltrosBusqueda{Consulta: c.consulta})
		if total != c.total || len(res) == 0 || res[0].ID != c.primero {
			var ids []string
			for _, r := range res {
				ids = append(ids, r.ID)
			}
			t.Errorf("%q: obtuve %v (total %d), esperaba primero %s y total %d", c.consulta, ids, total, c.primero, c.total)
		}
	}
}
