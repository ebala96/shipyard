// Shared UI primitives used across all pages

export function Button({ children, onClick, variant = 'primary', size = 'md', disabled = false, className = '' }) {
  const base = 'inline-flex items-center justify-center font-medium rounded-lg transition-colors focus:outline-none disabled:opacity-50 disabled:cursor-not-allowed'
  const sizes = { sm: 'px-3 py-1.5 text-xs', md: 'px-4 py-2 text-sm', lg: 'px-5 py-2.5 text-base' }
  const variants = {
    primary:   'bg-indigo-600 hover:bg-indigo-700 text-white',
    secondary: 'bg-gray-700 hover:bg-gray-600 text-gray-100',
    danger:    'bg-red-600 hover:bg-red-700 text-white',
    ghost:     'bg-transparent hover:bg-gray-800 text-gray-300',
  }
  return (
    <button
      onClick={onClick}
      disabled={disabled}
      className={`${base} ${sizes[size]} ${variants[variant]} ${className}`}
    >
      {children}
    </button>
  )
}

export function Badge({ children, color = 'gray' }) {
  const colors = {
    gray:   'bg-gray-700 text-gray-300',
    green:  'bg-green-900 text-green-300',
    red:    'bg-red-900 text-red-300',
    yellow: 'bg-yellow-900 text-yellow-300',
    indigo: 'bg-indigo-900 text-indigo-300',
    blue:   'bg-blue-900 text-blue-300',
  }
  return (
    <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-medium ${colors[color]}`}>
      {children}
    </span>
  )
}

export function Card({ children, className = '' }) {
  return (
    <div className={`bg-gray-900 border border-gray-800 rounded-xl p-5 ${className}`}>
      {children}
    </div>
  )
}

export function Spinner({ size = 'md' }) {
  const sizes = { sm: 'w-4 h-4', md: 'w-6 h-6', lg: 'w-8 h-8' }
  return (
    <div className={`${sizes[size]} border-2 border-gray-600 border-t-indigo-500 rounded-full animate-spin`} />
  )
}

export function PageHeader({ title, subtitle, action }) {
  return (
    <div className="flex items-start justify-between mb-6">
      <div>
        <h1 className="text-xl font-semibold text-gray-100">{title}</h1>
        {subtitle && <p className="text-sm text-gray-400 mt-1">{subtitle}</p>}
      </div>
      {action && <div>{action}</div>}
    </div>
  )
}

export function EmptyState({ title, subtitle, action }) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-center">
      <div className="w-12 h-12 bg-gray-800 rounded-xl mb-4 flex items-center justify-center">
        <span className="text-2xl">⚓</span>
      </div>
      <h3 className="text-gray-300 font-medium mb-1">{title}</h3>
      {subtitle && <p className="text-gray-500 text-sm mb-4">{subtitle}</p>}
      {action}
    </div>
  )
}

export function statusColor(status) {
  switch (status) {
    case 'running':  return 'green'
    case 'exited':   return 'red'
    case 'paused':   return 'yellow'
    case 'created':  return 'blue'
    default:         return 'gray'
  }
}
