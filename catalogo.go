package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// URL del recurso CSV del dataset "Catálogo de trámites y servicios del Estado" (AGESIC)
// en https://catalogodatos.gub.uy/dataset/agesic-guia-de-tramites. Se actualiza a diario.
const urlCSVPorDefecto = "https://catalogodatos.gub.uy/dataset/4249fa91-4e43-4428-be26-3e8c1400ed30/resource/41b02575-5cd6-461c-ab45-a54e93d751be/download/tramites.csv"

const (
	ttlCache = 24 * time.Hour
	// Tiempo máximo que una consulta espera a una fuente que todavía se está descargando.
	esperaMaxima = 20 * time.Second
)

// Tramite es una ficha de trámite. Los nombres siguen el diccionario de datos de AGESIC
// (metadatos-tramites.json); las fuentes que no son el CSV se convierten a este formato.
type Tramite struct {
	ID                    string
	Fuente                string
	Nombre                string
	InstitucionOID        string
	Institucion           string
	InstitucionPadreOID   string
	InstitucionPadre      string
	Creado                string
	Actualizado           string
	URL                   string
	QueEs                 string
	Casuistica            string
	RequisitosGenerales   string
	TieneCosto            string // "1" = tiene costo; vacío = sin costo o no informado
	Unidad                string
	Valor                 string
	CostoTexto            string // costo como texto libre (fuentes sin unidad/valor)
	OtrosCostos           string
	OtrosDatosDeInteres   string
	ComoSeHace            string // procedimiento general (fuentes sin detalle por canal)
	CanalesDeAtencion     string
	InternetRequisitos    string
	InternetURL           string
	InternetComoSeHace    string
	PersonaRequisitos     string
	PersonaComoSeHace     string
	PersonaDirecciones    string
	PersonaOtrasDirecc    string
	TelefonoRequisitos    string
	TelefonoComoSeHace    string
	MasInfoVinculos       string
	MasInfoAdjuntos       string
	EmailDeConsulta       string
	CategoriasDeOrganismo string

	// Campos normalizados (minúsculas, sin tildes) para búsqueda.
	nombreN, institucionN, textoN string
	siglas                        []string // siglas de institución y organismo padre ("bps", "mec")
}

// indexar calcula los campos normalizados usados por la búsqueda.
func (t *Tramite) indexar() {
	t.nombreN = normalizar(t.Nombre)
	t.institucionN = normalizar(t.Institucion + " " + t.InstitucionPadre + " " + t.CategoriasDeOrganismo)
	t.textoN = normalizar(t.QueEs)
	t.siglas = []string{siglas(t.Institucion), siglas(t.InstitucionPadre)}
}

// columnas mapea cada nombre de columna del CSV al campo correspondiente.
func (t *Tramite) columnas() map[string]*string {
	return map[string]*string{
		"id":                                   &t.ID,
		"nombre_tramite":                       &t.Nombre,
		"institucion_oid":                      &t.InstitucionOID,
		"institucion_nombre":                   &t.Institucion,
		"institucion_padre_organizacional_oid": &t.InstitucionPadreOID,
		"institucion_padre_organizacional_nombre": &t.InstitucionPadre,
		"creado":                   &t.Creado,
		"actualizado":              &t.Actualizado,
		"url":                      &t.URL,
		"ques_es":                  &t.QueEs,
		"casuistica":               &t.Casuistica,
		"requisitos_generales":     &t.RequisitosGenerales,
		"tiene_costo":              &t.TieneCosto,
		"unidad":                   &t.Unidad,
		"valor":                    &t.Valor,
		"otros_costos":             &t.OtrosCostos,
		"otros_datos_de_interes":   &t.OtrosDatosDeInteres,
		"canales_de_atencion":      &t.CanalesDeAtencion,
		"internet_requisitos":      &t.InternetRequisitos,
		"internet_url_del_tramite": &t.InternetURL,
		"internet_como_se_hace":    &t.InternetComoSeHace,
		"persona_requisitos":       &t.PersonaRequisitos,
		"persona_como_se_hace":     &t.PersonaComoSeHace,
		"persona_direcciones_listado_de_direcciones": &t.PersonaDirecciones,
		"persona_direcciones_otras_direcciones":      &t.PersonaOtrasDirecc,
		"telefono_requisitos":                        &t.TelefonoRequisitos,
		"telefono_como_se_hace":                      &t.TelefonoComoSeHace,
		"mas_informacion_vinculos":                   &t.MasInfoVinculos,
		"mas_informacion_adjuntos":                   &t.MasInfoAdjuntos,
		"email_de_consulta":                          &t.EmailDeConsulta,
		"categorias_de_organismo":                    &t.CategoriasDeOrganismo,
	}
}

// Datos es una instantánea del catálogo combinado de todas las fuentes.
type Datos struct {
	Tramites []*Tramite
	PorID    map[string][]*Tramite // un id puede tener varias fichas (casuísticas)
	Avisos   []string              // fuentes que no están disponibles en esta instantánea
}

func nuevosDatos(tramites []*Tramite) *Datos {
	d := &Datos{Tramites: tramites, PorID: make(map[string][]*Tramite, len(tramites))}
	for _, t := range tramites {
		d.PorID[t.ID] = append(d.PorID[t.ID], t)
	}
	return d
}

// fuente es un origen de trámites con caché propia en disco. Cada fuente se descarga
// y refresca de forma independiente, en segundo plano.
type fuente struct {
	nombre    string
	rutaCache string
	// descargar trae los datos crudos tal como se guardan en la caché.
	descargar func(ctx context.Context) ([]byte, error)
	// parsear convierte los datos crudos (descargados o de la caché) en trámites.
	parsear func(crudo []byte) ([]*Tramite, error)
	log     *slog.Logger

	mu          sync.Mutex
	tramites    []*Tramite
	cargado     time.Time
	cargando    bool
	ultimoError error
	// noAntesDe evita reintentar descargas fallidas en cada consulta.
	noAntesDe time.Time
}

type estadoFuente struct {
	tramites []*Tramite
	cargando bool
	err      error
}

func (f *fuente) estado() estadoFuente {
	f.mu.Lock()
	defer f.mu.Unlock()
	return estadoFuente{f.tramites, f.cargando, f.ultimoError}
}

// asegurar lanza una carga en segundo plano si no hay datos o están vencidos.
// Mientras tanto se siguen sirviendo los datos anteriores.
func (f *fuente) asegurar() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cargando || time.Now().Before(f.noAntesDe) || (f.tramites != nil && time.Since(f.cargado) < ttlCache) {
		return
	}
	f.cargando = true
	sinDatos := f.tramites == nil
	go f.cargar(sinDatos)
}

func (f *fuente) cargar(sinDatos bool) {
	tramites, cargado, err := f.obtener(sinDatos)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cargando = false
	f.ultimoError = err
	if err != nil {
		f.log.Warn("no se pudo cargar la fuente", "fuente", f.nombre, "error", err)
		espera := time.Minute
		if f.tramites != nil {
			espera = 15 * time.Minute // hay datos (vencidos) para seguir sirviendo
		}
		f.noAntesDe = time.Now().Add(espera)
		return
	}
	f.tramites, f.cargado = tramites, cargado
	f.log.Info("fuente cargada", "fuente", f.nombre, "tramites", len(tramites))
}

// obtener usa la caché en disco si está vigente; si no, descarga. Si la descarga falla
// y no hay datos en memoria, cae a la caché en disco aunque esté vencida.
func (f *fuente) obtener(sinDatos bool) ([]*Tramite, time.Time, error) {
	info, errStat := os.Stat(f.rutaCache)
	if errStat == nil && time.Since(info.ModTime()) < ttlCache {
		if ts, err := f.leerCache(); err == nil {
			return ts, info.ModTime(), nil
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	ts, errDescarga := f.descargarYGuardar(ctx)
	if errDescarga == nil {
		return ts, time.Now(), nil
	}

	if sinDatos && errStat == nil {
		if ts, err := f.leerCache(); err == nil {
			f.log.Warn("usando caché vencida", "fuente", f.nombre, "error", errDescarga)
			// Marcar como recién cargado evita reintentar la descarga en cada consulta.
			return ts, time.Now(), nil
		}
	}
	return nil, time.Time{}, errDescarga
}

func (f *fuente) leerCache() ([]*Tramite, error) {
	crudo, err := os.ReadFile(f.rutaCache)
	if err != nil {
		return nil, err
	}
	return f.parsear(crudo)
}

func (f *fuente) descargarYGuardar(ctx context.Context) ([]*Tramite, error) {
	crudo, err := f.descargar(ctx)
	if err != nil {
		return nil, err
	}
	ts, err := f.parsear(crudo)
	if err != nil {
		return nil, fmt.Errorf("datos inválidos: %w", err)
	}
	// Guardar en disco sólo si parseó bien; escritura atómica.
	if err := os.MkdirAll(filepath.Dir(f.rutaCache), 0o755); err == nil {
		tmp := f.rutaCache + ".tmp"
		if err := os.WriteFile(tmp, crudo, 0o644); err == nil {
			if err := os.Rename(tmp, f.rutaCache); err != nil {
				f.log.Warn("no se pudo guardar la caché", "fuente", f.nombre, "error", err)
			}
		}
	}
	return ts, nil
}

// Catalogo combina las fuentes de trámites.
type Catalogo struct {
	fuentes []*fuente
	espera  time.Duration
}

func dirCache() string {
	if dir := os.Getenv("TRAMITES_CACHE_DIR"); dir != "" {
		return dir
	}
	dir, err := os.UserCacheDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "mcp-tramites-gub-uy")
}

func NuevoCatalogo(log *slog.Logger) *Catalogo {
	cliente := &http.Client{Timeout: 2 * time.Minute}
	urlCSV := os.Getenv("TRAMITES_CSV_URL")
	if urlCSV == "" {
		urlCSV = urlCSVPorDefecto
	}
	urlIM := os.Getenv("TRAMITES_IM_URL")
	if urlIM == "" {
		urlIM = urlIMPorDefecto
	}
	return &Catalogo{
		fuentes: []*fuente{
			fuenteAGESIC(urlCSV, filepath.Join(dirCache(), "tramites.csv"), cliente, log),
			fuenteMontevideo(urlIM, filepath.Join(dirCache(), "montevideo.json"), cliente, log),
		},
		espera: esperaMaxima,
	}
}

func fuenteAGESIC(url, rutaCache string, cliente *http.Client, log *slog.Logger) *fuente {
	return &fuente{
		nombre:    "Catálogo de trámites del Estado (AGESIC)",
		rutaCache: rutaCache,
		log:       log,
		descargar: func(ctx context.Context) ([]byte, error) { return descargarURL(ctx, cliente, url) },
		parsear: func(crudo []byte) ([]*Tramite, error) {
			d, err := Parsear(bytes.NewReader(crudo))
			if err != nil {
				return nil, err
			}
			return d.Tramites, nil
		},
	}
}

func descargarURL(ctx context.Context, cliente *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mcp-tramites-gub-uy")
	resp, err := cliente.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d al descargar %s", resp.StatusCode, url)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// Precargar inicia la carga de todas las fuentes sin esperar.
func (c *Catalogo) Precargar() {
	for _, f := range c.fuentes {
		f.asegurar()
	}
}

// Obtener combina los trámites de todas las fuentes disponibles. Espera hasta c.espera
// a las fuentes que se están cargando por primera vez; si alguna no llega, la omite
// y lo informa en Datos.Avisos.
func (c *Catalogo) Obtener(ctx context.Context) (*Datos, error) {
	c.Precargar()
	limite := time.Now().Add(c.espera)
	for {
		pendiente := false
		for _, f := range c.fuentes {
			if e := f.estado(); e.tramites == nil && e.cargando {
				pendiente = true
			}
		}
		if !pendiente || time.Now().After(limite) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}

	var todos []*Tramite
	var avisos []string
	for _, f := range c.fuentes {
		e := f.estado()
		switch {
		case e.tramites != nil:
			todos = append(todos, e.tramites...)
		case e.cargando:
			avisos = append(avisos, fmt.Sprintf("Los trámites de %s todavía se están descargando; volvé a consultar en un minuto para incluirlos.", f.nombre))
		case e.err != nil:
			avisos = append(avisos, fmt.Sprintf("No se pudieron obtener los trámites de %s: %v", f.nombre, e.err))
		}
	}
	if len(todos) == 0 {
		return nil, fmt.Errorf("no hay datos de trámites disponibles: %v", avisos)
	}
	d := nuevosDatos(todos)
	d.Avisos = avisos
	return d, nil
}

// Parsear lee el CSV de AGESIC (con o sin BOM UTF-8) y arma los índices.
func Parsear(r io.Reader) (*Datos, error) {
	contenido, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	contenido = bytes.TrimPrefix(contenido, []byte("\xef\xbb\xbf"))

	lector := csv.NewReader(bytes.NewReader(contenido))
	lector.FieldsPerRecord = -1
	lector.LazyQuotes = true

	cabecera, err := lector.Read()
	if err != nil {
		return nil, fmt.Errorf("leyendo cabecera: %w", err)
	}
	indice := make(map[string]int, len(cabecera))
	for i, nombre := range cabecera {
		indice[nombre] = i
	}
	for _, requerida := range []string{"id", "nombre_tramite", "institucion_nombre"} {
		if _, ok := indice[requerida]; !ok {
			return nil, fmt.Errorf("falta la columna %q", requerida)
		}
	}

	var tramites []*Tramite
	for {
		fila, err := lector.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		t := &Tramite{Fuente: "AGESIC"}
		for nombre, campo := range t.columnas() {
			if i, ok := indice[nombre]; ok && i < len(fila) {
				*campo = fila[i]
			}
		}
		if t.ID == "" {
			continue
		}
		t.indexar()
		tramites = append(tramites, t)
	}
	if len(tramites) == 0 {
		return nil, fmt.Errorf("el CSV no contiene trámites")
	}
	return nuevosDatos(tramites), nil
}
