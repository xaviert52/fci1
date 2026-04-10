package main

import (
	"encoding/json"
	"log"
	"net/http"
)

func handleEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if r.Header.Get("Content-Type") != "application/json" {
		w.WriteHeader(http.StatusUnsupportedMediaType)
		return
	}

	var env Envelope
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(&env); err != nil {
		log.Printf(`{"level":"error","msg":"invalid_json","error":"%s"}`, err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	if err := validateEnvelope(env); err != nil {
		log.Printf(
			`{"level":"error","msg":"invalid_envelope","error":"%s","event_id":"%s"}`,
			err,
			env.ID,
		)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	key := makeIdempotencyKey(
		env.Type,
		env.Data.IdentityID,
		env.Data.FlowID,
	)

	if idemCache.seen(key) {
		log.Printf(
			`{"level":"info","msg":"duplicate_event","type":"%s","event_id":"%s","identity_id":"%s","request_id":"%s"}`,
			env.Type,
			env.ID,
			env.Data.IdentityID,
			env.Trace.RequestID,
		)
		w.WriteHeader(http.StatusAccepted)
		return
	}

	log.Printf(
		`{"level":"info","msg":"event_received","type":"%s","event_id":"%s","identity_id":"%s","request_id":"%s"}`,
		env.Type,
		env.ID,
		env.Data.IdentityID,
		env.Trace.RequestID,
	)

	// Lógica original (logs y auditoría interna en consola/Fargate)
	dispatchNotification(env)

	// =========================================================================
	// NUEVA INTEGRACIÓN B2B: Notificar a notify_service
	// =========================================================================

	// Verificamos si el evento de Kratos es un registro exitoso o un login
	// (Dependiendo de la versión de Kratos, el type puede variar ligeramente)
	if env.Type == "registration.successful" || env.Type == "identity.registration.successful" {

		// TODO: IMPORTANTE - ASIGNACIÓN DE VARIABLES
		// Como desconozco la estructura interna exacta de tu archivo `envelope.go`,
		// debes mapear el correo y el nombre desde el objeto `env`.
		// Ejemplo: userEmail := env.Data.Identity.Traits["email"].(string)
		userEmail := "correo@del.usuario" // REEMPLAZA ESTO CON LA RUTA REAL EN TU STRUCT
		userName := "Usuario"             // REEMPLAZA ESTO CON LA RUTA REAL EN TU STRUCT

		sendInternalNotification(EmailPayload{
			Type:       "email",
			Recipient:  userEmail,
			Subject:    "Bienvenido a Primecore",
			TemplateID: "welcome_user",
			Data: map[string]string{
				"user": userName,
			},
		})

	} else if env.Type == "login.successful" || env.Type == "identity.login.successful" {

		// Opcional: Enviar alerta de inicio de sesión exitoso al usuario
		userEmail := "correo@del.usuario" // REEMPLAZA ESTO CON LA RUTA REAL EN TU STRUCT

		sendInternalNotification(EmailPayload{
			Type:       "email",
			Recipient:  userEmail,
			Subject:    "Nuevo inicio de sesión detectado",
			TemplateID: "login_alert",
			Data:       map[string]string{},
		})
	}

	w.WriteHeader(http.StatusAccepted)
}
