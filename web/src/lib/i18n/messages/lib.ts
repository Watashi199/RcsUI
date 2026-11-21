// Messages de l'espace de noms « lib ». Clés préfixées par « lib. ».
// `en` doit contenir exactement les clés de `fr` (vérifié par TypeScript).

export const fr = {
	// Erreur de validation d'un champ (errorMessage)
	'lib.api.fieldError': '{field} : {message}'
} satisfies Record<string, string>;

export const en: Record<keyof typeof fr, string> = {
	'lib.api.fieldError': '{field}: {message}'
};
