"""Offline regression contracts for the committed Keycloak profile."""
import json
from pathlib import Path
import unittest

ROOT=Path(__file__).resolve().parent.parent

class ProfileTests(unittest.TestCase):
    def test_clients_are_scoped_and_secret_free(self):
        realm=json.loads((ROOT/'examples/identity/keycloak-realm.json').read_text())
        self.assertNotIn('users',realm)
        clients={c['clientId']:c for c in realm['clients']}
        self.assertTrue(clients['gateway']['bearerOnly'])
        for name in ('openwebui','gateway-console'):
            client=clients[name]
            self.assertNotIn('secret',client)
            self.assertIn('basic',client['defaultClientScopes'])
            self.assertFalse(client['publicClient'])
            self.assertFalse(client['directAccessGrantsEnabled'])
            self.assertFalse(client['serviceAccountsEnabled'])
            self.assertFalse(client['fullScopeAllowed'])
            self.assertEqual(client['attributes']['pkce.code.challenge.method'],'S256')
            self.assertTrue(all(uri.startswith('https://') and '*' not in uri for uri in client['redirectUris']))
            audience=next(m for m in client['protocolMappers'] if m['protocolMapper']=='oidc-audience-mapper')
            self.assertEqual(audience['config']['included.client.audience'],'gateway')
            self.assertEqual(audience['config']['id.token.claim'],'false')
            self.assertEqual(audience['config']['access.token.claim'],'true')
        self.assertNotEqual(clients['openwebui']['redirectUris'],clients['gateway-console']['redirectUris'])

if __name__=='__main__':unittest.main()
