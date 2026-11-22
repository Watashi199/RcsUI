// Messages de l'espace de noms « remotes ». Clés préfixées par « remotes. ».
// `en` doit contenir exactement les clés de `fr` (vérifié par TypeScript).

export const fr = {
	// Liste
	'remotes.list.pageTitle': 'Remotes · RcsUI',
	'remotes.list.title': 'Remotes rclone',
	'remotes.list.new': 'Nouveau remote',
	'remotes.list.empty': 'Aucun remote.',
	'remotes.list.emptyAccessible': 'Aucun remote accessible.',
	'remotes.list.colName': 'Nom',
	'remotes.list.colType': 'Type',
	'remotes.list.colTest': 'Test',
	'remotes.list.testing': 'test…',
	'remotes.list.testOk': 'OK · {ms} ms',
	'remotes.list.testFailed': 'Échec : {error}',
	'remotes.list.test': 'Tester',
	'remotes.list.deleteConfirm': 'Supprimer le remote « {name} » ?',

	// Détail
	'remotes.detail.pageTitle': '{name} · Remotes · RcsUI',
	'remotes.detail.title': 'Remote « {name} »',
	'remotes.detail.operators': 'Opérateurs autorisés',
	'remotes.detail.grantsSaved': 'Accès enregistrés.',
	'remotes.detail.noOperators': 'Aucun utilisateur de rôle « operator ».',
	'remotes.detail.saveGrants': 'Enregistrer les accès',
	'remotes.detail.grantsHint': 'Les administrateurs ont accès à tous les remotes ; les lecteurs à aucun.',

	// Création
	'remotes.new.pageTitle': 'Nouveau remote · RcsUI',
	'remotes.new.title': 'Nouveau remote',

	// Formulaire
	'remotes.form.step': 'Étape de configuration',
	'remotes.form.oauthBefore': "Ce backend utilise OAuth. Sur un poste disposant d'un navigateur, exécutez",
	'remotes.form.oauthAfter': 'puis collez ici le jeton JSON obtenu.',
	'remotes.form.continue': 'Continuer',
	'remotes.form.name': 'Nom du remote',
	'remotes.form.nameHint': 'Lettres, chiffres, « _ », « - », « . » et espaces.',
	'remotes.form.type': 'Type',
	'remotes.form.filterPlaceholder': 'Filtrer (s3, drive, sftp…)',
	'remotes.form.filterLabel': 'Filtrer les types',
	'remotes.form.params': 'Paramètres {type}',
	'remotes.form.advanced': 'Options avancées',
	'remotes.form.secretUnchanged': '•••••• (inchangé)',
	'remotes.form.boolDefault': 'défaut ({value})',
	'remotes.form.create': 'Créer le remote'
} satisfies Record<string, string>;

export const en: Record<keyof typeof fr, string> = {
	'remotes.list.pageTitle': 'Remotes · RcsUI',
	'remotes.list.title': 'rclone remotes',
	'remotes.list.new': 'New remote',
	'remotes.list.empty': 'No remotes.',
	'remotes.list.emptyAccessible': 'No accessible remotes.',
	'remotes.list.colName': 'Name',
	'remotes.list.colType': 'Type',
	'remotes.list.colTest': 'Test',
	'remotes.list.testing': 'testing…',
	'remotes.list.testOk': 'OK · {ms} ms',
	'remotes.list.testFailed': 'Failed: {error}',
	'remotes.list.test': 'Test',
	'remotes.list.deleteConfirm': 'Delete remote “{name}”?',

	'remotes.detail.pageTitle': '{name} · Remotes · RcsUI',
	'remotes.detail.title': 'Remote “{name}”',
	'remotes.detail.operators': 'Authorized operators',
	'remotes.detail.grantsSaved': 'Access saved.',
	'remotes.detail.noOperators': 'No users with the “operator” role.',
	'remotes.detail.saveGrants': 'Save access',
	'remotes.detail.grantsHint': 'Administrators can access all remotes; viewers can access none.',

	'remotes.new.pageTitle': 'New remote · RcsUI',
	'remotes.new.title': 'New remote',

	'remotes.form.step': 'Configuration step',
	'remotes.form.oauthBefore': 'This backend uses OAuth. On a machine with a web browser, run',
	'remotes.form.oauthAfter': 'then paste the resulting JSON token here.',
	'remotes.form.continue': 'Continue',
	'remotes.form.name': 'Remote name',
	'remotes.form.nameHint': 'Letters, digits, “_”, “-”, “.” and spaces.',
	'remotes.form.type': 'Type',
	'remotes.form.filterPlaceholder': 'Filter (s3, drive, sftp…)',
	'remotes.form.filterLabel': 'Filter types',
	'remotes.form.params': '{type} parameters',
	'remotes.form.advanced': 'Advanced options',
	'remotes.form.secretUnchanged': '•••••• (unchanged)',
	'remotes.form.boolDefault': 'default ({value})',
	'remotes.form.create': 'Create remote'
};
