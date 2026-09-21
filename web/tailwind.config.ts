import type { Config } from 'tailwindcss'

export default <Partial<Config>>{
  content: [
    './app/**/*.{vue,js,ts}',
    './components/**/*.{vue,js,ts}',
    './composables/**/*.{js,ts}',
    './layouts/**/*.{vue,js,ts}',
    './pages/**/*.{vue,js,ts}',
    './stores/**/*.{js,ts}',
    './types/**/*.ts'
  ],
  theme: {
    extend: {
      fontFamily: {
        sans: ['"Plus Jakarta Sans Variable"', '"Plus Jakarta Sans"', '"Manrope Variable"', 'Manrope', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        logo: ['"Source Sans 3 Variable"', '"Source Sans 3"', '"Manrope Variable"', 'Manrope', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        editorial: ['"Source Serif 4"', 'Georgia', '"Times New Roman"', 'serif'],
        accent: ['"Source Sans 3 Variable"', '"Source Sans 3"', '"Manrope Variable"', 'Manrope', 'ui-sans-serif', 'system-ui', 'sans-serif']
      },
      boxShadow: {
        panel: '0 25px 80px rgba(2, 6, 23, 0.45)'
      }
    }
  }
}
