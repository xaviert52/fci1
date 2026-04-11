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

	// Lógica original (logs y auditoría interna)
	dispatchNotification(env)

	// =========================================================================
	// NUEVA INTEGRACIÓN B2B: Notificar a notify_service
	// =========================================================================

	// Extracción segura del email desde los traits
	userEmail, emailOk := env.Data.Traits["email"].(string)

	if !emailOk || userEmail == "" {
		log.Printf(`{"level":"warn","msg":"missing_email_in_traits","identity_id":"%s"}`, env.Data.IdentityID)
	} else {
		userName := "Usuario" // Fallback seguro

		// Intentamos extraer el nombre de forma dinámica
		if nameMap, ok := env.Data.Traits["name"].(map[string]interface{}); ok {
			if first, ok := nameMap["first"].(string); ok {
				userName = first
			}
		} else if nameStr, ok := env.Data.Traits["name"].(string); ok {
			userName = nameStr
		}

		if env.Type == "registration.successful" || env.Type == "identity.registration.successful" {
			sendInternalNotification(NotificationPayload{
				Type:      "email",
				Recipient: userEmail,
				Subject:   "Bienvenido a Primecore",
				Body:      "Hola " + userName + ", ¡bienvenido a la plataforma Primecore! Tu registro ha sido exitoso.",
			})

		} else if env.Type == "login.successful" || env.Type == "identity.login.successful" {
			sendInternalNotification(NotificationPayload{
				Type:      "email",
				Recipient: userEmail,
				Subject:   "Nuevo inicio de sesión detectado",
				Body:      "Hola " + userName + ", hemos detectado un nuevo inicio de sesión exitoso en tu cuenta de Primecore.",
			})
		}
	}

	w.WriteHeader(http.StatusAccepted)
}
