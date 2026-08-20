import js from '@eslint/js';
import ts from 'typescript-eslint';
import svelte from 'eslint-plugin-svelte';

export default [
  { ignores: ['dist/', 'node_modules/', '.svelte-kit/', 'coverage/'] },
  js.configs.recommended,
  ...ts.configs.recommended,
  ...svelte.configs['flat/recommended'],
  {
    languageOptions: {
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
