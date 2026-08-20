import js from '@eslint/js';
import ts from 'typescript-eslint';
import svelte from 'eslint-plugin-svelte';
import globals from 'globals';

export default [
  { ignores: ['../internal/assets/dist/', 'node_modules/', '.svelte-kit/', 'coverage/'] },
  js.configs.recommended,
  ...ts.configs.recommended,
  ...svelte.configs['flat/recommended'],
  {
    languageOptions: {
      // The application runs in a browser. The service worker used by trusted
      // mode runs in a worker, so both global sets are declared.
      globals: { ...globals.browser, ...globals.serviceworker },
      parserOptions: { extraFileExtensions: ['.svelte'] },
    },
    rules: {
      // Principle I: nothing may reach outside the local network. An absolute
      // URL in the bundle is almost always a mistake, so make it loud.
      'no-restricted-syntax': [
        'error',
        {
          selector: "Literal[value=/^https?:\\/\\/(?!localhost|127\\.0\\.0\\.1)/]",
          message:
            'Absolute external URL in the bundle. Principle I forbids any request leaving the local network.',
        },
      ],
    },
  },
  {
    files: ['**/*.svelte'],
    languageOptions: {
      parserOptions: { parser: ts.parser },
    },
  },
];
